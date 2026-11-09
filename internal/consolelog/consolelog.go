// Package consolelog keeps the last server log lines in memory so the dashboard can show and stream them.
// Every line is also printed to stderr, so the terminal output is unchanged.
package consolelog

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const MaxLines = 1000

type Entry struct {
	ID    uint64    `json:"id"`
	Time  time.Time `json:"t"`
	Level string    `json:"level"` // INFO | WARN | ERROR
	Msg   string    `json:"msg"`
}

// Event is what subscribers receive: new entries, or a notice that the buffer was cleared.
type Event struct {
	Clear   bool
	Entries []Entry
}

type Buffer struct {
	mu      sync.Mutex
	max     int
	next    uint64
	entries []Entry
	subs    map[chan Event]struct{}

	file     *os.File // see Persist
	path     string
	maxBytes int64
	size     int64
}

func New(max int) *Buffer { return &Buffer{max: max, subs: map[chan Event]struct{}{}} }

// Default is the process-wide buffer.
var Default = New(MaxLines)

// Logf records a line under a tag, e.g. Logf("WARN", "UPSTREAM", "key %s cooling", name), and prints it to stderr.
func Logf(level, tag, format string, args ...any) {
	Default.Add(level, "["+tag+"] "+fmt.Sprintf(format, args...))
}

func (b *Buffer) Add(level, msg string) {
	log.Printf("%-5s %s", level, msg)
	b.mu.Lock()
	b.next++
	e := Entry{ID: b.next, Time: time.Now(), Level: level, Msg: msg}
	b.entries = append(b.entries, e)
	if len(b.entries) > b.max {
		b.entries = append([]Entry(nil), b.entries[len(b.entries)-b.max:]...)
	}
	b.write(e)
	b.publish(Event{Entries: []Entry{e}})
	b.mu.Unlock()
}

// publish must run with mu held. A subscriber that cannot keep up is dropped (its channel closes) and
// reconnects for a fresh snapshot, so a slow browser never blocks logging.
func (b *Buffer) publish(ev Event) {
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			delete(b.subs, ch)
			close(ch)
		}
	}
}

func (b *Buffer) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Entry{}, b.entries...)
}

func (b *Buffer) Clear() {
	b.mu.Lock()
	b.entries = nil
	b.truncate()
	b.publish(Event{Clear: true})
	b.mu.Unlock()
}

// Subscribe returns the current entries and a channel of what follows, atomically (no gap, no duplicate).
// Call cancel when done.
func (b *Buffer) Subscribe() (snapshot []Entry, events <-chan Event, cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 256)
	b.subs[ch] = struct{}{}
	return append([]Entry{}, b.entries...), ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
}
