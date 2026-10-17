package server

import (
	"time"

	"github.com/Autumn-27/artex/config"
)

// sqliteWriteTimeout 是**后台持久化写操作**的统一超时下限。
//
// 为什么必须比 busy 预算长：SQLite 单写者模型下，遇到库级写锁的写语句会在
// busy_timeout（config.SQLiteBusyTimeout，由 DSN 下发）内等待锁、而不是立刻
// 失败。如果调用方的 context 比这个等待预算短，一次「排队本来会成功」的写入
// 就会被打断成 "context deadline exceeded"——写入没写成、日志还刷出一条，
// 而真正的问题只是写锁在另一条语句手里多待了几秒。
//
// 使用范围是「丢了就不可恢复」的后台写：旁问快照刷盘、检查点落盘、环境快照、
// 通知事件。这些调用都在后台 goroutine 或事件钩子里，多等几秒远好过丢一条
// 用户可见的状态记录；取值是 busy 预算 + 5 秒余量，覆盖 BEGIN 等待之后的语句
// 执行与提交。请求路径（r.Context()）不套用该下限：那里的等待预算是调用方的。
const sqliteWriteTimeout = config.SQLiteBusyTimeout + 5*time.Second
