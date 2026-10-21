package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// 主库的空闲页回收。
//
// SQLite 的 DELETE 不缩小文件：它只把腾空的页挂到 freelist 上，留给后续 INSERT
// 复用。三种 auto_vacuum 取值的差别正是「谁把页还给文件系统」：
//
//   - NONE（SQLite 默认，也是本项目历史库的实际取值）：永不归还。文件大小等于
//     历史峰值的最大值，与当前实际存了多少行无关。
//   - INCREMENTAL：允许归还，但搬运与截断仍要显式执行 incremental_vacuum。
//   - FULL：每次提交都搬页，代价是每次写入都变慢，不适合桌面单机的高频写入。
//
// ARTEX 的主库一直在用 NONE，而它恰好有两条持续制造空闲页的写入路径：
//
//  1. checkpoints 每次写入都会删掉该 scope 上超出 checkpointPerScope 的旧快照
//     （见 db/checkpoints.go）。快照是 JSON 文本，单条可能几十 KB，按 scope 反复
//     滚动就等价于一台持续把页送进 freelist 的机器。
//  2. 人工保留策略（db/retention.go）会按表批量删除 usage、日志与终态通知。
//
// 回收缺位时这些页就永久留在文件里。实测某安装的 artex.db：168,644,608 字节中
// 有 37,684/41,173 页（91.5%）停在 freelist 上，有效数据只有约 13 MB。
//
// 流量索引库（traffic/traffic.go 的 initIndex / reclaimChunk / compactIndex）早已
// 按这套契约处理；主库此前是产品里唯一没有该契约的 SQLite 文件，这里补齐：
// 新库在首次建表前就带上 INCREMENTAL，老库在启动时一次性转换，此后每次删除都
// 做有界回收。

const (
	// sqliteAutoVacuumIncremental 是 SQLite 中 auto_vacuum=incremental 的数值。
	sqliteAutoVacuumIncremental = 2

	// reclaimChunkPages 是一次 incremental_vacuum 最多搬动的页数（4 KiB 页下约
	// 8 MiB）。搬页要持有写锁，块小则单次停顿短。
	reclaimChunkPages = 2048

	// reclaimMinPages 是触发回收的阈值：空闲页少于这个数时不值得去抢写锁，留给
	// 下一次删除一起收（4 KiB 页下约 2 MiB）。
	reclaimMinPages = 512

	// reclaimMaxSteps 是单次回收的步数上限，避免一次调用把写者锁住太久。没收完
	// 的部分留给下一次删除，最终收敛。
	reclaimMaxSteps = 8

	// reclaimBudget 是单次回收的时间预算。
	reclaimBudget = 3 * time.Second

	// autoVacuumConvertLimit 限制启动期一次性转换的库规模。VACUUM 的耗时与临时
	// 空间都随存量数据线性增长，超过这个规模的库不在启动路径上重写，改由
	// DB.CompactStorage 人工触发。
	autoVacuumConvertLimit = int64(1) << 30 // 1 GiB
)

// sqliteHasTables reports whether the file already contains any table.
func sqliteHasTables(ctx context.Context, con *sql.Conn) (bool, error) {
	var n int
	if err := con.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// sqliteAutoVacuumMode reads the mode recorded in the file header.
func sqliteAutoVacuumMode(ctx context.Context, con *sql.Conn) (int, error) {
	var mode int
	if err := con.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return 0, err
	}
	return mode, nil
}

// sqliteFileBytes returns the file size as page_size × page_count.
func sqliteFileBytes(ctx context.Context, con *sql.Conn) (int64, error) {
	var pageSize, pageCount int64
	if err := con.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	if err := con.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, err
	}
	return pageSize * pageCount, nil
}

// prepareSQLiteAutoVacuum turns on incremental reclaim for a database that has no
// tables yet.
//
// auto_vacuum 只有在文件还没有任何表或索引时才能直接写进头部；建过表之后要改
// 就必须走一次 VACUUM（见 convertSQLiteAutoVacuum）。所以这一步必须排在
// applySQLiteSchema 之前——新库因此从诞生起就是 INCREMENTAL，之后不需要任何动作。
//
// 写法与 traffic.initIndex 一致：先设 pragma，再用一次 VACUUM 让头部落盘（空库上
// VACUUM 是瞬时的）。
func prepareSQLiteAutoVacuum(ctx context.Context, con *sql.Conn) error {
	hasTables, err := sqliteHasTables(ctx, con)
	if err != nil {
		return err
	}
	if hasTables {
		return nil
	}
	for _, stmt := range []string{`PRAGMA auto_vacuum=incremental`, `VACUUM`} {
		if _, err := con.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("新库启用增量回收: %w", err)
		}
	}
	return nil
}

// convertSQLiteAutoVacuum rewrites a legacy (auto_vacuum=NONE) file once, and
// sweeps any free pages an already-converted file is still carrying.
//
// 只在库里已经有表、且头部仍是 NONE 时做整体重写：这正是老安装升级后的第一次
// 启动。转换成功后 INCREMENTAL 会永久写进文件头，之后不会再进来。
//
// 失败不让启动失败——库仍然可用，只是这轮没有转换，下次启动会重试。
func convertSQLiteAutoVacuum(ctx context.Context, con *sql.Conn) error {
	hasTables, err := sqliteHasTables(ctx, con)
	if err != nil {
		return err
	}
	if !hasTables {
		// 空文件：prepareSQLiteAutoVacuum 已经处理，这里无需重写。
		return nil
	}
	mode, err := sqliteAutoVacuumMode(ctx, con)
	if err != nil {
		return err
	}
	if mode != sqliteAutoVacuumIncremental {
		size, err := sqliteFileBytes(ctx, con)
		if err != nil {
			return err
		}
		if size > autoVacuumConvertLimit {
			log.Printf("[db] 主库 auto_vacuum=%d 且文件 %d 字节，超过启动期转换上限 %d 字节；"+
				"本次跳过，可用 DB.CompactStorage 人工回收", mode, size, autoVacuumConvertLimit)
			return nil
		}
		if err := compactSQLiteFile(ctx, con); err != nil {
			return err
		}
		after, sizeErr := sqliteFileBytes(ctx, con)
		if sizeErr != nil {
			after = 0
		}
		log.Printf("[db] 主库已启用增量回收：auto_vacuum=%d→%d，文件 %d→%d 字节（回收 %d 字节）",
			mode, sqliteAutoVacuumIncremental, size, after, size-after)
	}
	// 已转换的库也可能带着历史空闲页（上次回收被时间预算截断，或转换后从未发生
	// 删除）。启动时清一次，仍然有界。
	freed, err := reclaimSQLiteFreePages(ctx, con)
	if err != nil {
		return err
	}
	if freed > 0 {
		if _, err := con.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return fmt.Errorf("截断主库 WAL: %w", err)
		}
		log.Printf("[db] 启动期回收 %d 个空闲页", freed)
	}
	return nil
}

// compactSQLiteFile performs the full rewrite that actually returns pages to the
// filesystem, and converts a legacy file on the way.
//
// PRAGMA auto_vacuum 只有紧随其后的 VACUUM 才会把它写进头部，两步必须落在同一条
// 连接上——池化的 *sql.DB 可能把它们分派到不同连接，那样设置会被静默丢弃
// （traffic.initIndex 的注释记录了同一个坑）。
func compactSQLiteFile(ctx context.Context, con *sql.Conn) error {
	if _, err := con.ExecContext(ctx, `PRAGMA auto_vacuum=incremental`); err != nil {
		return fmt.Errorf("启用增量回收: %w", err)
	}
	if _, err := con.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("压实主库: %w", err)
	}
	// WAL 模式下滑走的页先记在日志里；只有 TRUNCATE 检查点能让缩小在磁盘上可见。
	if _, err := con.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("截断主库 WAL: %w", err)
	}
	return nil
}

// reclaimSQLiteFreePages runs the bounded incremental_vacuum loop and reports how
// many pages it managed to hand back.
//
// 步数与块大小都有上限：搬页要持写锁，一次性清空几十万页会把交互式写入卡住。
// 没搬完的部分留给下一次删除——删除会持续产生空闲页，回收也就持续跟上。
func reclaimSQLiteFreePages(ctx context.Context, con *sql.Conn) (int, error) {
	var free int
	if err := con.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
		return 0, err
	}
	if free < reclaimMinPages {
		return 0, nil
	}
	reclaimed := 0
	for step := 0; step < reclaimMaxSteps && free >= reclaimMinPages; step++ {
		before := free
		if _, err := con.ExecContext(ctx, fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, reclaimChunkPages)); err != nil {
			return reclaimed, fmt.Errorf("回收空闲页: %w", err)
		}
		if err := con.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
			return reclaimed, err
		}
		reclaimed += before - free
		if free >= before {
			// 搬不动了：剩余空闲页不在文件尾部，本轮到此为止。
			break
		}
	}
	return reclaimed, nil
}

// VacuumInto 把当前库整体快照到 dst(dst 必须是不存在的路径)。
//
// 这是 SQLite 官方推荐的在线备份方式:VACUUM INTO 在一个读事务里生成一致的、已整理
// 的副本——不需要停写,也不会像直接复制文件那样把 -wal 里尚未 checkpoint 的提交漏掉。
// 失败时可能留下半成品文件,由调用方清理(这里不替调用方猜要不要删)。
func (d *DB) VacuumInto(ctx context.Context, dst string) error {
	if dst == "" {
		return fmt.Errorf("vacuum into: 目标路径为空")
	}
	if _, err := d.ExecContext(ctx, `VACUUM INTO $1`, dst); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dst, err)
	}
	return nil
}

// ReclaimFreePages hands pages freed by a committed delete back to the filesystem.
//
// 最佳努力、有界、可重入：调用方在删除提交之后调用它，只记录失败——绝不能因为
// 它回滚已经提交的删除。空闲页少于 reclaimMinPages 时直接返回，不为几页去抢
// 写锁。老库（auto_vacuum=NONE）上 incremental_vacuum 是**静默空操作**，这里先
// 确认模式，避免制造「已回收」的假象。
func (d *DB) ReclaimFreePages() {
	ctx, cancel := context.WithTimeout(context.Background(), reclaimBudget)
	defer cancel()

	conn, err := d.Conn(ctx)
	if err != nil {
		log.Printf("[db] 回收空闲页：获取连接失败：%v", err)
		return
	}
	defer conn.Close()

	mode, err := sqliteAutoVacuumMode(ctx, conn)
	if err != nil {
		log.Printf("[db] 回收空闲页：读取 auto_vacuum 失败：%v", err)
		return
	}
	if mode != sqliteAutoVacuumIncremental {
		return
	}
	if _, err := reclaimSQLiteFreePages(ctx, conn); err != nil {
		log.Printf("[db] 回收空闲页失败：%v", err)
		return
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		log.Printf("[db] 回收空闲页：截断 WAL 失败：%v", err)
	}
}

// CompactStorage rewrites the main database once: every free page goes back to the
// filesystem and a legacy file is converted to auto_vacuum=incremental.
//
// 人工入口，与启动期自动转换同一套实现，供超大库或运维场景使用。调用方必须
// 接受它会持有写锁数秒（耗时与存量数据大小线性相关），因此不挂在请求路径上。
func (d *DB) CompactStorage(ctx context.Context) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return compactSQLiteFile(ctx, conn)
}
