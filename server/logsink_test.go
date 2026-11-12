package server

import (
	"io"
	"sync"
	"testing"
	"time"
)

// newTestSink builds an in-memory log sink detached from the global logger, so
// these tests neither pollute /api/logs (and its ring) nor need a DB.
func newTestSink() *logSinkT {
	return &logSinkT{cap: 64, subs: map[chan LogLine]struct{}{}, out: io.Discard}
}

// TestLogSinkUnsubscribeDuringFanout pins the fix for a fatal panic. add() used to
// snapshot the subscriber set under the lock, release it, and only then deliver;
// unsubscribe deleted the entry under the lock and closed the channel *after*
// releasing it. A delivery could therefore target an already-closed channel
// ("send on closed channel"). That panics the goroutine that called log.Printf,
// and most ARTEX log calls come from engine/planner/worker goroutines with no
// recover — so a browser closing the /system/logs tab could kill the process.
func TestLogSinkUnsubscribeDuringFanout(t *testing.T) {
	// Hold a pool of live subscribers so every fan-out has many channels to
	// deliver to: that widens the window in which a delivery can be attempted
	// while another goroutine is closing one of them.
	const (
		pool    = 64
		writers = 8
		rounds  = 4000
	)
	sink := newTestSink()

	// churn keeps the pool topped up while constantly unsubscribing members —
	// exactly what repeated SSE reconnects (tab refresh, proxy timeout) do.
	var churnWG sync.WaitGroup
	churnWG.Add(1)
	stop := make(chan struct{})
	go func() {
		defer churnWG.Done()
		type sub struct {
			ch    <-chan LogLine
			unsub func()
		}
		live := make([]sub, 0, pool)
		for i := 0; i < pool; i++ {
			ch, unsub := sink.subscribe()
			live = append(live, sub{ch, unsub})
		}
		for {
			select {
			case <-stop:
				for _, s := range live {
					s.unsub()
				}
				return
			default:
			}
			i := len(live) - 1
			live[i].unsub()
			ch, unsub := sink.subscribe()
			live[i] = sub{ch, unsub}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				sink.add(LogLine{Text: "line"})
			}
		}()
	}
	wg.Wait()
	close(stop)
	churnWG.Wait()

	sink.mu.Lock()
	left := len(sink.subs)
	sink.mu.Unlock()
	if left != 0 {
		t.Fatalf("subscribers leaked after unsubscribe: %d", left)
	}
}

// TestLogSinkSubscribeCloseProtocol keeps the reader contract that streamLogs
// relies on: every delivered line is observed, and the channel reports closed
// (`l, ok := <-ch` with ok=false) once unsubscribed, so the handler returns
// instead of blocking on a live channel forever.
func TestLogSinkSubscribeCloseProtocol(t *testing.T) {
	sink := newTestSink()
	ch, unsub := sink.subscribe()

	sink.add(LogLine{Text: "before"})
	select {
	case l := <-ch:
		if l.Text != "before" {
			t.Fatalf("unexpected delivery: %+v", l)
		}
	case <-time.After(time.Second):
		t.Fatal("no delivery to subscriber")
	}

	unsub()
	if _, ok := <-ch; ok {
		t.Fatal("channel still open after unsubscribe")
	}

	// Idempotent: the SSE handler defers unsub and a second call must not panic
	// (closing an already-closed channel would).
	unsub()

	// A closed, unsubscribed channel must never be delivered to again.
	sink.add(LogLine{Text: "after"})
	if _, ok := <-ch; ok {
		t.Fatal("delivery to an unsubscribed channel")
	}
}
