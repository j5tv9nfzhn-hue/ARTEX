//go:build windows

package main

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql 驱动（驱动名 "pgx"）
)

// 本文件负责判断"数据库到底能不能连"，这是自举能否继续的前提。
// 复用项目已有的 pgx 驱动，不引入第二个数据库客户端依赖。

// pgTarget 承载从 DSN 解析出的连接目标，仅用于日志与错误提示。
type pgTarget struct {
	host string
	port string
}

// parsePostgresURL 解析 DSN，兼容两种写法：
//   - postgres://user:pass@host:port/db?sslmode=disable（ARTEX_PG_DSN 常用）
//   - host=... port=... user=...（config.json 字段拼接后可能出现）
//
// 解析不出来时回落为 127.0.0.1:5432 —— 与 config.buildDSN 的默认值一致，
// 保证错误提示指向的地址和实际会连的地址一致。
func parsePostgresURL(dsn string) *pgTarget {
	t := &pgTarget{host: "127.0.0.1", port: "5432"}

	if u, err := url.Parse(dsn); err == nil && strings.HasPrefix(dsn, "postgres://") {
		if h := u.Hostname(); h != "" {
			t.host = h
		}
		if p := u.Port(); p != "" {
			t.port = p
		}
		return t
	}

	for _, kv := range strings.Fields(dsn) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, "'\"")
		switch k {
		case "host":
			if v != "" {
				t.host = v
			}
		case "port":
			if v != "" {
				t.port = v
			}
		}
	}
	return t
}

// addr 拼接成 host:port。
func (t *pgTarget) addr() string { return net.JoinHostPort(t.host, t.port) }

// hostPortOf 从 DSN 提取 host:port，仅用于日志与错误提示。
func hostPortOf(dsn string) string { return parsePostgresURL(dsn).addr() }

// postgresReachable 用一次真实的、带超时的连接判断数据库是否可用。
// 比单纯探测 TCP 端口更严格：端口开着但服务尚未完成初始化时会返回 false，
// 从而避免"自举已启动数据库"被误判成假阳性。
func postgresReachable(dsn string) bool {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var one int
	return db.QueryRowContext(ctx, "select 1").Scan(&one) == nil
}
