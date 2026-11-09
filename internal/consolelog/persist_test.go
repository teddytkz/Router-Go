package consolelog

import (
	"path/filepath"
	"testing"
)

func TestPersistSurvivesRestartRotatesAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "console.log")
	a := New(5)
	if err := a.Persist(path, 0); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"one", "two", "three"} {
		a.Add("INFO", m)
	}
	b := New(5) // a restart
	if err := b.Persist(path, 0); err != nil {
		t.Fatal(err)
	}
	if got := b.Snapshot(); len(got) != 3 || got[0].Msg != "one" || got[2].Msg != "three" {
		t.Fatalf("after restart: %+v", got)
	}
	b.Add("WARN", "four")
	if got := b.Snapshot(); got[3].Msg != "four" || got[3].ID <= got[2].ID {
		t.Errorf("ids must keep growing: %+v", got)
	}
	b.Clear()
	c := New(5)
	c.Persist(path, 0)
	if len(c.Snapshot()) != 0 {
		t.Errorf("clear left lines in the file: %+v", c.Snapshot())
	}

	r := New(5)
	r.Persist(path, 200) // tiny cap: rotates
	for range 10 {
		r.Add("INFO", "a line that is long enough to rotate the file soon")
	}
	r.mu.Lock()
	size := r.size
	r.mu.Unlock()
	if size > 400 {
		t.Errorf("file not rotated: %d bytes", size)
	}
}
