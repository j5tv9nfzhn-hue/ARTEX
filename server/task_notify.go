package server

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/notify"
)

// 本文件是**任务级事件**的 emit 收敛点：task_status_changed（任务完成/失败/
// 超时/暂停）与 approval_pending（等待人工审批）。审批侧的写入在 intercept 包
// （那里才拿得到 pending id），任务状态的写入全部汇入本文件。
//
// 为什么收敛在 Manager 的三个状态写库函数：任务状态的所有生产路径——超时收尾、
// planner 判定目标达成、用户/编排暂停、Token 预算自动暂停——都经由
// SetTaskStatusGuarded / SetTaskStatus / ApplyTaskPauseReason 落库，把「真实
// 状态变化才发」的判定放在这里，才能保证不论来源、不论重复调用，事件不重不漏。

// notifyEmitTimeout 限制事件写库的最长耗时。emit 是 best-effort 旁路：
// 它绝不能因为数据库慢而拖住任务状态迁移的调用方。
const notifyEmitTimeout = 5 * time.Second

// emitTaskStatusEvent 在任务状态**真实变化**后写入 task_status_changed 事件。
//
// 只接受四个值（done/failed/timeout/paused），与通知领域对外承诺的事件面
// 严格一致；调用方传别的状态（如 created/running）直接忽略。
//
// best-effort：写库失败只记日志。事件丢了顶多少一条通知，而状态迁移是任务
// 生命周期的事实——绝不能让「通知写失败」反向影响状态本身。
func (m *Manager) emitTaskStatusEvent(id int64, status, reason string) {
	switch status {
	case "done", "failed", "timeout", "paused":
	default:
		return
	}
	snap := notify.Snapshot{
		Kind:         notify.EventTaskStatusChanged,
		TaskID:       id,
		TaskTitle:    m.taskTitleFor(id),
		TaskStatus:   status,
		StatusReason: reason,
	}
	// finding_id=0：任务事件没有漏洞关联（列是非空 BIGINT，0 是合法占位）。
	m.recordNotificationEvent(notify.EventTaskStatusChanged, 0, id, snap)
}

// recordNotificationEvent 把事件写入 notification_events 的唯一出口。
//
// 用 context.Background() 而不是调用方 ctx：状态迁移发生在引擎收尾、超时
// 定时器等会随任务结束而取消的上下文里，而事件必须在「状态已改」之后独立
// 落库；从调用方 ctx 继承取消/超时会把事件跟着任务一起取消掉。
func (m *Manager) recordNotificationEvent(kind string, findingID, taskID int64, snap notify.Snapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyEmitTimeout)
	defer cancel()
	if _, err := m.pg.AddNotificationEvent(ctx, kind, findingID, snap); err != nil {
		log.Printf("[notify] 写入 %s 事件失败 task=%d（不影响任务状态本身）: %v", kind, taskID, err)
	}
}

// taskTitleFor 尽力取任务展示标题：内存句柄最新且免回查，缺失时回查数据库
// （name 优先、空回退 description）。取不到就返回空串，渲染端会退化为
// 「任务 #N」——通知的可用性不依赖这次查询成功。
func (m *Manager) taskTitleFor(id int64) string {
	if t, ok := m.Task(strconv.FormatInt(id, 10)); ok {
		if s := strings.TrimSpace(t.Name); s != "" {
			return s
		}
		if s := strings.TrimSpace(t.Description); s != "" {
			return s
		}
	}
	title, err := m.pg.TaskTitle(id)
	if err != nil {
		log.Printf("[notify] 查询任务标题失败 task=%d: %v", id, err)
		return ""
	}
	return title
}

// terminalStatusReason 给终态补充一句人话原因。done 无需解释（正常完成）；
// timeout 必须说明「是时限收尾而不是任务失败」——否则收到通知的人会把
// 系统主动收尾误读成异常，这正是「人必须知道」要避免的歧义。
func terminalStatusReason(status string) string {
	if status == "timeout" {
		return "任务达到时限自动收尾，目标未全部达成"
	}
	return ""
}

// pauseCauseReason 把暂停原因错误转成给用户看的一句话：暂停来源（用户/编排/
// 预算闸）都登记为 *agent.AbortCause，取其 Short；认不出时退回通用文案，
// 不让通知因为拿不到细因而整条不发。
func pauseCauseReason(cause error) string {
	var ac *agent.AbortCause
	if errors.As(cause, &ac) && ac.Short != "" {
		return ac.Short
	}
	return "任务已暂停"
}
