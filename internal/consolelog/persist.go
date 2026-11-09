package consolelog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// Persist keeps the log across restarts: every new line is appended to path as one JSON object per line, and the
// last MaxLines lines already in the file are loaded into the buffer now. At maxBytes the file is rotated to
// path+".1" (one old file is kept). Clear also empties the file.
func (b *Buffer) Persist(path string, maxBytes int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var old []Entry
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Msg != "" {
				old = append(old, e)
			}
		}
		f.Close()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(old) > b.max {
		old = old[len(old)-b.max:]
	}
	for i := range old {
		b.next++
		old[i].ID = b.next
	}
	b.entries = append(old, b.entries...)
	b.file, b.path, b.maxBytes = f, path, maxBytes
	if st, err := f.Stat(); err == nil {
		b.size = st.Size()
	}
	return nil
}

// write appends e to the file (mu held). A disk problem must never break logging, so errors are dropped.
func (b *Buffer) write(e Entry) {
	if b.file == nil {
		return
	}
	line, _ := json.Marshal(e)
	line = append(line, '\n')
	n, err := b.file.Write(line)
	if err != nil {
		return
	}
	b.size += int64(n)
	if b.maxBytes > 0 && b.size > b.maxBytes {
		b.file.Close()
		_ = os.Rename(b.path, b.path+".1")
		if f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			b.file, b.size = f, 0
		} else {
			b.file = nil
		}
	}
}

func (b *Buffer) truncate() {
	if b.file != nil {
		_ = b.file.Truncate(0)
		b.size = 0
	}
}
