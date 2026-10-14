package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const MaxTaskCategoryNameRunes = 80

// MaxTaskCategoryBatchSize bounds one batch move so a single request cannot lock
// an unbounded number of task rows.
const MaxTaskCategoryBatchSize = 100

var (
	ErrTaskCategoryInvalid      = errors.New("invalid task category")
	ErrTaskCategoryNameConflict = errors.New("task category name already exists")
	ErrTaskCategoryNotFound     = errors.New("task category not found")
	ErrTaskCategoryTaskNotFound = errors.New("task not found")
)

// TaskCategory is a globally reusable task grouping label.
type TaskCategory struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	NKey      string    `json:"-"`
	TaskCount int       `json:"task_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const taskCategoryCols = `category.id, category.name, category.nkey,
       count(task.id) FILTER (WHERE task.deleted_at IS NULL),
       category.created_at, category.updated_at`

func scanTaskCategory(row interface{ Scan(...any) error }) (TaskCategory, error) {
	var category TaskCategory
	err := row.Scan(&category.ID, &category.Name, &category.NKey, &category.TaskCount, &category.CreatedAt, &category.UpdatedAt)
	return category, err
}

func normalizeTaskCategoryName(name string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", "", fmt.Errorf("%w: name is required", ErrTaskCategoryInvalid)
	}
	if utf8.RuneCountInString(name) > MaxTaskCategoryNameRunes {
		return "", "", fmt.Errorf("%w: name exceeds %d characters", ErrTaskCategoryInvalid, MaxTaskCategoryNameRunes)
	}
	return name, strings.ToLower(name), nil
}

func taskCategoryUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (d *DB) CreateTaskCategory(name string) (*TaskCategory, error) {
	name, nkey, err := normalizeTaskCategoryName(name)
	if err != nil {
		return nil, err
	}
	// 顶层 INSERT ... RETURNING：SQLite 的 CTE 里不能放 DML（原语句是
	// `WITH inserted AS (INSERT ... RETURNING *) SELECT ... FROM inserted`，
	// 会报 near "INSERT"）。等价性与逐列对照见 db/dialect_dml.go 的
	// taskCategoryInsertQuery。冲突判定由 RETURNING 的返回行数给出：
	// 0 行 → sql.ErrNoRows → ErrTaskCategoryNameConflict。
	category, err := scanTaskCategory(d.QueryRow(taskCategoryInsertQuery, name, nkey))
	if err == sql.ErrNoRows {
		return nil, ErrTaskCategoryNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (d *DB) ListTaskCategories() ([]*TaskCategory, error) {
	rows, err := d.Query(`
SELECT ` + taskCategoryCols + `
FROM task_categories category
LEFT JOIN tasks task ON task.category_id=category.id
GROUP BY category.id
ORDER BY category.name, category.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	categories := []*TaskCategory{}
	for rows.Next() {
		category, err := scanTaskCategory(rows)
		if err != nil {
			return nil, err
		}
		categories = append(categories, &category)
	}
	return categories, rows.Err()
}

func (d *DB) GetTaskCategory(id int64) (*TaskCategory, error) {
	category, err := scanTaskCategory(d.QueryRow(`
SELECT `+taskCategoryCols+`
FROM task_categories category
LEFT JOIN tasks task ON task.category_id=category.id
WHERE category.id=$1
GROUP BY category.id`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (d *DB) RenameTaskCategory(id int64, name string) (*TaskCategory, error) {
	name, nkey, err := normalizeTaskCategoryName(name)
	if err != nil {
		return nil, err
	}
	category, err := scanTaskCategory(d.QueryRow(taskCategoryRenameQuery, id, name, nkey))
	if err == sql.ErrNoRows {
		return nil, ErrTaskCategoryNotFound
	}
	if taskCategoryUniqueViolation(err) {
		return nil, ErrTaskCategoryNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &category, nil
}

// DeleteTaskCategory moves affected tasks to the uncategorized bucket through
// the tasks.category_id ON DELETE SET NULL foreign key.
func (d *DB) DeleteTaskCategory(id int64) (bool, error) {
	result, err := d.Exec(`DELETE FROM task_categories WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// SetTaskCategory updates one live task. A nil category means uncategorized.
func (d *DB) SetTaskCategory(taskID int64, categoryID *int64) (*TaskCategory, error) {
	if categoryID == nil {
		result, err := d.Exec(`UPDATE tasks SET category_id=NULL WHERE id=$1 AND deleted_at IS NULL`, taskID)
		if err != nil {
			return nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows == 0 {
			return nil, ErrTaskCategoryTaskNotFound
		}
		return nil, nil
	}
	if *categoryID <= 0 {
		return nil, fmt.Errorf("%w: category id must be positive", ErrTaskCategoryInvalid)
	}
	// 0 行 = 「任务不存在且已删除」或「分类不存在」，两个引擎都收敛到这条路径，
	// 由 taskCategoryAssignMissing 区分。
	if isPostgresEngine() {
		category, err := scanTaskCategory(d.QueryRow(taskCategoryAssignPG, taskID, *categoryID))
		if err == sql.ErrNoRows {
			return nil, d.taskCategoryAssignMissing(taskID)
		}
		if err != nil {
			return nil, err
		}
		return &category, nil
	}
	// SQLite 的 CTE 里不能放 DML，塌成同一事务里的两条语句：先挂分类，再读回
	// 改完之后口径的分类行。第 1 步是写语句，事务一开就拿到写锁，两步之间没有
	// 别的连接插进来。等价性论证见 db/dialect_dml.go 的 taskCategoryAssignSQLite*。
	return d.setTaskCategorySQLite(taskID, *categoryID)
}

// setTaskCategorySQLite 是 SetTaskCategory 在 SQLite 上的两语句等价物。
func (d *DB) setTaskCategorySQLite(taskID, categoryID int64) (*TaskCategory, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var movedID int64
	err = tx.QueryRow(taskCategoryAssignSQLiteUpdate, taskID, categoryID).Scan(&movedID)
	if err == sql.ErrNoRows {
		// 未挂上：不提交，让 defer 里的 Rollback 收掉这个事务。
		return nil, d.taskCategoryAssignMissing(taskID)
	}
	if err != nil {
		return nil, err
	}
	category, err := scanTaskCategory(tx.QueryRow(taskCategoryAssignSQLiteSelect, categoryID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &category, nil
}

// taskCategoryAssignMissing 判定「一条都没改」到底是任务没了还是分类没了，
// 与两个引擎的 0 行路径共用。
func (d *DB) taskCategoryAssignMissing(taskID int64) error {
	var taskExists bool
	if checkErr := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&taskExists); checkErr != nil {
		return checkErr
	}
	if !taskExists {
		return ErrTaskCategoryTaskNotFound
	}
	return ErrTaskCategoryNotFound
}

// SetTasksCategory moves several tasks into one category (nil = uncategorized)
// inside a single transaction, so a half-applied batch is never observable.
// It returns the ids that were actually updated — ids missing from that slice
// were deleted between selection and submit — plus the refreshed category row
// whose task_count already reflects this move.
func (d *DB) SetTasksCategory(taskIDs []int64, categoryID *int64) ([]int64, *TaskCategory, error) {
	if len(taskIDs) == 0 {
		return nil, nil, fmt.Errorf("%w: task ids are required", ErrTaskCategoryInvalid)
	}
	if len(taskIDs) > MaxTaskCategoryBatchSize {
		return nil, nil, fmt.Errorf("%w: at most %d tasks per request", ErrTaskCategoryInvalid, MaxTaskCategoryBatchSize)
	}
	for _, id := range taskIDs {
		if id <= 0 {
			return nil, nil, fmt.Errorf("%w: task id must be positive", ErrTaskCategoryInvalid)
		}
	}
	if categoryID != nil && *categoryID <= 0 {
		return nil, nil, fmt.Errorf("%w: category id must be positive", ErrTaskCategoryInvalid)
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Checking the category inside the transaction keeps a concurrent delete from
	// turning the UPDATE below into a foreign key violation.
	if categoryID != nil {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_categories WHERE id=$1)`, *categoryID).Scan(&exists); err != nil {
			return nil, nil, err
		}
		if !exists {
			return nil, nil, ErrTaskCategoryNotFound
		}
	}
	// = ANY($1::bigint[]) 只有 PostgreSQL 有（SQLite 既无 ANY 也绑不了
	// []int64），SQLite 侧展开成 IN ($2,...)；语句与参数顺序见
	// db/dialect_dml.go:taskCategoryBatchMoveStmt。
	args := []any{}
	rows, err := tx.Query(taskCategoryBatchMoveStmt(&args, taskIDs, categoryID), args...)
	if err != nil {
		return nil, nil, err
	}
	updated := make([]int64, 0, len(taskIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		updated = append(updated, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var category *TaskCategory
	if categoryID != nil {
		fetched, err := scanTaskCategory(tx.QueryRow(`
SELECT `+taskCategoryCols+`
FROM task_categories category
LEFT JOIN tasks task ON task.category_id=category.id
WHERE category.id=$1
GROUP BY category.id`, *categoryID))
		if err != nil {
			return nil, nil, err
		}
		category = &fetched
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return updated, category, nil
}
