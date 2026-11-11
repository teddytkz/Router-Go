package sysstat

import (
	"testing"
	"time"
)

func TestSnapshotDescribesOnlyThisProcess(t *testing.T) {
	s := &Sampler{}
	snap := s.Snapshot()
	c := snap.Current
	if c.RSS == 0 || snap.TotalRAM < c.RSS || snap.GoHeap == 0 || snap.CPUs < 1 || snap.Goroutines < 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if c.CPU < 0 || c.CPU > 100 || time.Since(c.T) > time.Minute || len(snap.History) != 1 {
		t.Fatalf("current = %+v history=%d", c, len(snap.History))
	}
	s.sample()
	s.sample()
	if got := len(s.Snapshot().History); got != 3 {
		t.Fatalf("history = %d, want 3", got)
	}
	for range history + 5 {
		s.sample()
	}
	if got := len(s.Snapshot().History); got != history {
		t.Fatalf("history must be capped at %d, got %d", history, got)
	}
}
