// Package tracelog is the P0 session-semantics sidecar ledger (统一会话语义
// 旁路转录). It records orchestration-level events — thread/turn boundaries,
// intent claim/settle, tool call/result, steer/kill orders, direction-gate
// bumps, control actions, provider-call markers — as append-only JSONL, one
// file per thread under the store directory.
//
// Design contract (P0, 纯增量/禁止负优化):
//   - Everything is fail-silent: Emit never returns an error, never panics,
//     and never influences control flow. A dropped event is logged once at
//     most and forgotten.
//   - The ledger is observational only. It complements (and never touches)
//     norma/transcript raw LLM conversations and the db activities ledger.
//   - File names are derived from thread IDs with a conservative charset so
//     the same ID maps to the same file on Windows (no ':', '/', '\' etc.).
package tracelog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one semantic ledger entry. Thread file order is the event order;
// TS is RFC3339Nano.
type Event struct {
	TS       string `json:"ts"`
	Type     string `json:"type"`
	Thread   string `json:"thread"`
	Actor    string `json:"actor,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
	IntentID string `json:"intent_id,omitempty"`
	Round    int    `json:"round,omitempty"`
	Source   string `json:"source,omitempty"`
	Name     string `json:"name,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Err      bool   `json:"err,omitempty"`
}

// ThreadInfo summarises one thread file for listing/trace views.
type ThreadInfo struct {
	Thread  string `json:"thread"`
	Events  int    `json:"events"`
	Bytes   int64  `json:"bytes"`
	FirstTS string `json:"first_ts,omitempty"`
	LastTS  string `json:"last_ts,omitempty"`
}

// Store appends events to per-thread JSONL files. Safe for concurrent use.
// A nil *Store is a valid off-switch: every method is nil-safe.
type Store struct {
	dir   string
	mu    sync.Mutex
	files map[string]*os.File
	dead  bool // true after an unrecoverable write/open failure: drop silently
}

// NewStore prepares dir (best effort). Even if dir is unusable the returned
// store is safe to call — writes are dropped silently.
func NewStore(dir string) *Store {
	s := &Store{dir: dir, files: map[string]*os.File{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.dead = true
	}
	return s
}

// Close closes all open handles (process shutdown; optional in P0).
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		_ = f.Close()
	}
	s.files = map[string]*os.File{}
}

// Emit appends one event to its thread file. Fail-silent by contract.
func (s *Store) Emit(ev Event) {
	if s == nil || ev.Type == "" {
		return
	}
	ev.Thread = SafeName(ev.Thread)
	if ev.Thread == "" {
		return
	}
	if ev.TS == "" {
		ev.TS = time.Now().Format(time.RFC3339Nano)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return
	}
	f := s.fileFor(ev.Thread)
	if f == nil {
		return
	}
	if _, err := f.Write(line); err != nil {
		// One bad handle should not poison later writes of other threads;
		// drop this handle and continue silently.
		_ = f.Close()
		delete(s.files, ev.Thread)
	}
}

func (s *Store) fileFor(name string) *os.File {
	if f, ok := s.files[name]; ok {
		return f
	}
	f, err := os.OpenFile(filepath.Join(s.dir, name+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	s.files[name] = f
	return f
}

// SafeName maps a thread ID onto a conservative file-name charset
// [A-Za-z0-9._-]; anything else becomes '-'. Prevents path traversal and
// Windows-illegal names (':' would open an NTFS alternate data stream).
func SafeName(thread string) string {
	var b strings.Builder
	b.Grow(len(thread))
	for _, r := range thread {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), ".")
}

// Clip collapses newlines and trims s to n runes so Detail stays one readable
// snippet (the full payloads live in norma transcripts / activities anyway).
func Clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// ListThreads returns one entry per thread file, newest activity first.
func (s *Store) ListThreads() []ThreadInfo {
	out := []ThreadInfo{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	dir := s.dir
	s.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	type row struct {
		info ThreadInfo
		mod  time.Time
	}
	rows := []row{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		info := ThreadInfo{Thread: strings.TrimSuffix(e.Name(), ".jsonl")}
		if n, first, last := scanFile(filepath.Join(dir, e.Name())); n >= 0 {
			info.Events, info.FirstTS, info.LastTS = n, first, last
		}
		info.Bytes = fi.Size()
		rows = append(rows, row{info: info, mod: fi.ModTime()})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].mod.After(rows[j].mod) })
	for _, r := range rows {
		out = append(out, r.info)
	}
	return out
}

func scanFile(path string) (n int, first, last string) {
	f, err := os.Open(path)
	if err != nil {
		return -1, "", ""
	}
	defer f.Close()
	n = 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		n++
		var ev Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.TS != "" {
			if first == "" {
				first = ev.TS
			}
			last = ev.TS
		}
	}
	return n, first, last
}

// ReadEvents streams one thread's events from a line offset (replay cursor).
// Returns the events, the next offset, and the total line count.
func (s *Store) ReadEvents(thread string, offset, limit int) (events []Event, next, total int, err error) {
	events = []Event{}
	if s == nil {
		return events, 0, 0, os.ErrNotExist
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	s.mu.Lock()
	dir := s.dir
	s.mu.Unlock()

	name := SafeName(thread)
	if name == "" {
		return events, 0, 0, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(dir, name+".jsonl"))
	if err != nil {
		return events, 0, 0, err
	}
	defer f.Close()

	total = 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		idx := total
		total++
		if idx < offset {
			continue
		}
		if len(events) >= limit {
			continue // keep counting for total
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			events = append(events, ev)
		}
	}
	if err := sc.Err(); err != nil && len(events) == 0 {
		return events, 0, 0, err
	}
	next = offset + len(events)
	if next > total {
		next = total
	}
	return events, next, total, nil
}
