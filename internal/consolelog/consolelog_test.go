package consolelog

import (
	"testing"
	"time"
)

func TestBufferCapsClearsAndStreams(t *testing.T) {
	b := New(3)
	for _, m := range []string{"a", "b", "c", "d"} {
		b.Add("INFO", m)
	}
	got := b.Snapshot()
	if len(got) != 3 || got[0].Msg != "b" || got[2].Msg != "d" || got[2].ID != 4 {
		t.Fatalf("snapshot = %+v", got)
	}

	snap, ev, cancel := b.Subscribe()
	if len(snap) != 3 {
		t.Fatalf("subscribe snapshot = %d", len(snap))
	}
	b.Add("WARN", "e")
	if e := <-ev; len(e.Entries) != 1 || e.Entries[0].Msg != "e" || e.Entries[0].Level != "WARN" {
		t.Fatalf("event = %+v", e)
	}
	b.Clear()
	if e := <-ev; !e.Clear || len(b.Snapshot()) != 0 {
		t.Fatalf("clear event = %+v", e)
	}
	cancel()
	cancel() // idempotent
	if _, ok := <-ev; ok {
		t.Fatal("channel must be closed after cancel")
	}

	// a subscriber that never reads is dropped instead of blocking the logger
	_, slow, c2 := b.Subscribe()
	defer c2()
	done := make(chan struct{})
	go func() {
		for range 1000 {
			b.Add("INFO", "x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logging blocked on a slow subscriber")
	}
	n := 0
	for range slow {
		n++
	}
	if n == 0 || n > 256 {
		t.Fatalf("slow subscriber received %d events", n)
	}
}
