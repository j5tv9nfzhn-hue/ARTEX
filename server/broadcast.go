package server

import (
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// Broadcaster is a per-task in-process pub/sub for live activity events. The
// engine publishes each appended activity at its single emit point; SSE handlers
// subscribe per task. Storage (activity table) and the live stream come from the
// same Publish call, so they never diverge.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[string]map[chan db.Activity]struct{} // task id -> set of subscriber channels
	// live 是「尚未落库」的增量通道：与 subs 分开，是因为它的帧没有 id、不进游标，
	// 断了也不需要补偿（下一帧带的是全文）。
	live    map[string]map[chan LiveFrame]struct{}
	liveSeq map[string]int64
}

// LiveFrame 是一条实时增量。Text 是**累计全文**而不是片段：中间帧被节流丢掉也不会
// 丢字，界面永远用最新一帧整体替换。
type LiveFrame struct {
	Kind      string `json:"kind"` // thinking | text | tool_use
	Text      string `json:"text,omitempty"`
	Tool      string `json:"tool,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Worker    string `json:"worker,omitempty"`
	IntentID  int64  `json:"intent_id,omitempty"`
	Seq       int64  `json:"seq"`
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subs:    map[string]map[chan db.Activity]struct{}{},
		live:    map[string]map[chan LiveFrame]struct{}{},
		liveSeq: map[string]int64{},
	}
}

// SubscribeLive 订阅一个任务的实时增量（buffer 32：界面只关心最新一帧）。
func (b *Broadcaster) SubscribeLive(task string) (<-chan LiveFrame, func()) {
	ch := make(chan LiveFrame, 32)
	b.mu.Lock()
	if b.live[task] == nil {
		b.live[task] = map[chan LiveFrame]struct{}{}
	}
	b.live[task][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if m := b.live[task]; m != nil {
				delete(m, ch)
				if len(m) == 0 {
					delete(b.live, task)
					delete(b.liveSeq, task)
				}
			}
			b.mu.Unlock()
			close(ch)
		})
	}
}

// PublishLive 把一帧增量扇出给订阅者；seq 在锁内自增，保证同一任务单调。
func (b *Broadcaster) PublishLive(task string, f LiveFrame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.liveSeq[task]++
	f.Seq = b.liveSeq[task]
	for ch := range b.live[task] {
		select {
		case ch <- f:
		default: // 满了就丢：下一帧是全文，不会丢内容
		}
	}
}

// liveDeltaEvery 是增量节流窗口：约 25 帧/秒，肉眼连续，SSE 与浏览器也不会被刷爆。
const liveDeltaEvery = 40 * time.Millisecond

// liveEmitter 把 agent 的逐 token 增量压成「累计全文」帧。
type liveEmitter struct {
	bc       *Broadcaster
	task     string
	worker   string
	intentID int64

	mu   sync.Mutex
	buf  map[string]*strings.Builder
	last time.Time
}

func newLiveEmitter(bc *Broadcaster, task, worker string, intentID int64) func(agent.LiveDelta) {
	le := &liveEmitter{bc: bc, task: task, worker: worker, intentID: intentID, buf: map[string]*strings.Builder{}}
	return le.emit
}

func (l *liveEmitter) emit(d agent.LiveDelta) {
	if l == nil || l.bc == nil {
		return
	}
	now := time.Now()
	l.mu.Lock()
	switch d.Kind {
	case "thinking", "text":
		b := l.buf[d.Kind]
		if b == nil {
			b = &strings.Builder{}
			l.buf[d.Kind] = b
		}
		b.WriteString(d.Text)
		if now.Sub(l.last) < liveDeltaEvery {
			l.mu.Unlock()
			return
		}
		l.last = now
		frame := LiveFrame{Kind: d.Kind, Text: b.String(), Worker: l.worker, IntentID: l.intentID}
		l.mu.Unlock()
		l.bc.PublishLive(l.task, frame)
	case "tool_use":
		// 工具调用是一段新消息的边界：文本缓冲清零，避免下一轮 thinking 接着上一轮。
		l.buf = map[string]*strings.Builder{}
		l.last = now
		l.mu.Unlock()
		l.bc.PublishLive(l.task, LiveFrame{Kind: d.Kind, Tool: d.Tool, ToolUseID: d.ToolUseID, Worker: l.worker, IntentID: l.intentID})
	default:
		l.mu.Unlock() // 未知 kind：解锁后忽略，绝不能让锁漏出去
	}
}

// Subscribe returns a buffered channel of activities for a task plus an
// unsubscribe func the caller must invoke (defer) to release it.
func (b *Broadcaster) Subscribe(task string) (<-chan db.Activity, func()) {
	ch := make(chan db.Activity, 256)
	b.mu.Lock()
	if b.subs[task] == nil {
		b.subs[task] = map[chan db.Activity]struct{}{}
	}
	b.subs[task][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if m := b.subs[task]; m != nil {
				delete(m, ch)
				if len(m) == 0 {
					delete(b.subs, task)
				}
			}
			b.mu.Unlock()
			close(ch)
		})
	}
}

// Publish fans an activity out to all subscribers of a task. Non-blocking: if a
// subscriber's buffer is full the event is dropped — the client reconnects with
// its last seq cursor and catches up the gap from the DB, so liveness never
// stalls the engine.
func (b *Broadcaster) Publish(task string, a db.Activity) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[task] {
		select {
		case ch <- a:
		default:
		}
	}
}
