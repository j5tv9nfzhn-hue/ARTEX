package db

import (
	"fmt"

	"github.com/Autumn-27/artex/config"
)

// JSON 数组列的谓词构造（PostgreSQL ↔ SQLite）。
//
// 这些表达式属于资产查询 DSL（db/asset_dsl.go）——DSL 把用户输入编译成 SQL，
// 所以谓词是按引擎拼出来的，而不是写死在查询文本里。

// jsonArrayILikeExpr 构造「数组中任一元素模糊匹配」的谓词。
//
//	PostgreSQL: EXISTS (SELECT 1 FROM unnest(col) t(v) WHERE v ILIKE $N)
//	SQLite:     EXISTS (SELECT 1 FROM json_each(col) WHERE value LIKE $N)
//
// SQLite 的 LIKE 对 ASCII 本身就大小写不敏感，因此这里用 LIKE 而不是
// 显式做大小写折叠：对 ASCII 等价，而 PostgreSQL 的 ILIKE 对非 ASCII 会
// 按 C.UTF-8 折叠。这一点与 assets.go 里其余 ILIKE 列的处理保持一致，
// 不引入新的行为差异。
func jsonArrayILikeExpr(col, placeholder string) string {
	if config.Engine() == config.EnginePostgres {
		return "EXISTS (SELECT 1 FROM unnest(" + col + ") t(v) WHERE v ILIKE " + placeholder + ")"
	}
	return "EXISTS (SELECT 1 FROM json_each(" + col + ") WHERE value LIKE " + placeholder + ")"
}

// assetIDsAnyExpr 构造「该行属于给定 id 集合」的谓词。
//
//	PostgreSQL: id = ANY($1::bigint[])
//	SQLite:     id IN ($1,$2,...)
//
// 集合成员在 Go 侧展开成 IN 列表而不是绑定一个数组参数：SQLite 驱动无法把
// []int64 绑成单参（"unsupported type []int64"），而 ANY() 也不是 SQLite
// 函数。展开为占位符后每个值仍各自绑定，不存在拼接字面量的注入面。
func idSetInExpr(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = fmt.Sprintf("$%d", i+1)
	}
	return "id IN (" + joinNonEmpty(marks) + ")", args
}

// idSetAnyExpr 构造「该行是否属于给定的行 id 集合」，返回可直接拼进 WHERE
// 的片段，参数追加到 args 之后。
func idSetAnyExpr(args *[]any, ids []int64, prefix string) string {
	marks := make([]string, len(ids))
	for i, id := range ids {
		*args = append(*args, id)
		if prefix == "" {
			marks[i] = fmt.Sprintf("$%d", len(*args))
		} else {
			marks[i] = prefix + fmt.Sprintf("$%d", len(*args))
		}
	}
	return "id IN (" + joinNonEmpty(marks) + ")"
}

func joinNonEmpty(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
