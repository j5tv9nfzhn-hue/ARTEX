package server

import (
	"log"
	"runtime/debug"

	pgdb "github.com/Autumn-27/artex/db"
)

// skillHookMu guards the optional Task lifecycle hooks. They are deliberately
// stored on Manager (not Engine) because every terminal transition and every
// user deletion funnels through Manager's lifecycle methods.
func (m *Manager) SetTaskSkillHooks(
	terminal func(taskID, status string),
	deleted func(taskID string, snapshot *pgdb.Task),
) {
	if m == nil {
		return
	}
	m.skillHookMu.Lock()
	m.onTaskTerminal = terminal
	m.onTaskDeleted = deleted
	m.skillHookMu.Unlock()
}

// fireTaskTerminal runs the terminal hook on its own goroutine. Recover protects
// the lifecycle path from a curator panic; the task is already persisted.
func (m *Manager) fireTaskTerminal(taskID, status string) {
	if m == nil {
		return
	}
	m.skillHookMu.RLock()
	fn := m.onTaskTerminal
	m.skillHookMu.RUnlock()
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[skills] 任务 %s 自动沉淀 panic: %v\n%s", taskID, r, debug.Stack())
			}
		}()
		fn(taskID, status)
	}()
}

// fireTaskDeleted runs the deletion hook synchronously, before the cascade.
// The caller (DeleteTask) has not staged or removed any task data yet, so the
// curator can still read the exploration graph. Bounded collector work lives in
// the hook itself; a curator failure is logged and never cancels deletion.
func (m *Manager) fireTaskDeleted(taskID string, snapshot *pgdb.Task) {
	if m == nil || snapshot == nil {
		return
	}
	m.skillHookMu.RLock()
	fn := m.onTaskDeleted
	m.skillHookMu.RUnlock()
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[skills] 任务 %s 删除前自动沉淀 panic: %v\n%s", taskID, r, debug.Stack())
		}
	}()
	fn(taskID, snapshot)
}
