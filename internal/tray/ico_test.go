package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"
)

func TestIcoWrapsPNG(t *testing.T) {
	var p bytes.Buffer
	if err := png.Encode(&p, image.NewRGBA(image.Rect(0, 0, 32, 16))); err != nil {
		t.Fatal(err)
	}
	b := ico(p.Bytes())
	if len(b) != 22+p.Len() || binary.LittleEndian.Uint16(b[2:]) != 1 || b[6] != 32 || b[7] != 16 {
		t.Fatalf("bad header: % x", b[:22])
	}
	if size, off := binary.LittleEndian.Uint32(b[14:]), binary.LittleEndian.Uint32(b[18:]); int(size) != p.Len() || off != 22 || !bytes.Equal(b[off:], p.Bytes()) {
		t.Fatalf("size=%d offset=%d", size, off)
	}
	if got := ico([]byte("not a png")); string(got) != "not a png" {
		t.Fatal("non-PNG input must pass through")
	}
}
