package kiro

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"testing"
)

// encodeFrame builds one event stream frame: string headers (type 7), a raw payload, both CRCs.
func encodeFrame(headers map[string]string, payload []byte) []byte {
	var h bytes.Buffer
	for k, v := range headers {
		h.WriteByte(byte(len(k)))
		h.WriteString(k)
		h.WriteByte(7)
		_ = binary.Write(&h, binary.BigEndian, uint16(len(v)))
		h.WriteString(v)
	}
	return seal(h.Bytes(), payload)
}

func seal(headers, payload []byte) []byte {
	total := 16 + len(headers) + len(payload)
	b := make([]byte, 12, total)
	binary.BigEndian.PutUint32(b[0:], uint32(total))
	binary.BigEndian.PutUint32(b[4:], uint32(len(headers)))
	binary.BigEndian.PutUint32(b[8:], crc32.ChecksumIEEE(b[:8]))
	b = append(append(b, headers...), payload...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

// event is a frame of the given event type with a JSON payload.
func event(typ string, payload any) []byte {
	b, _ := json.Marshal(payload)
	return encodeFrame(map[string]string{":message-type": "event", ":event-type": typ}, b)
}

func TestReadFrame(t *testing.T) {
	// a frame with a boolean, an int32 and a string header: only the string one is kept, the others are skipped by size
	hdr := []byte{1, 'a', 0, 1, 'b', 1, 1, 'c', 4, 0, 0, 0, 7, 1, 'd', 7, 0, 2, 'o', 'k'}
	in := bytes.NewReader(append(seal(hdr, []byte(`{"x":1}`)), event("assistantResponseEvent", map[string]any{"content": "hi"})...))

	f, err := readFrame(in)
	if err != nil || f.headers["d"] != "ok" || len(f.headers) != 1 || string(f.payload) != `{"x":1}` {
		t.Fatalf("frame = %+v, %v", f, err)
	}
	f, err = readFrame(in)
	if err != nil || f.headers[":event-type"] != "assistantResponseEvent" || string(f.payload) != `{"content":"hi"}` {
		t.Fatalf("second frame = %+v, %v", f, err)
	}
	if _, err = readFrame(in); err != io.EOF {
		t.Fatalf("clean end: %v", err)
	}
}

func TestReadFrameIntegrity(t *testing.T) {
	good := event("x", map[string]any{"a": 1})
	flip := func(at int) []byte { b := append([]byte{}, good...); b[at] ^= 1; return b }
	oversized := append([]byte{}, good...) // headers claim more than the frame holds, with a valid prelude CRC: the bounds check must catch it
	binary.BigEndian.PutUint32(oversized[4:], 1<<20)
	binary.BigEndian.PutUint32(oversized[8:], crc32.ChecksumIEEE(oversized[:8]))

	cases := map[string]struct {
		data []byte
		want error // nil = any error but io.EOF
	}{
		"truncated mid-frame":  {good[:len(good)-3], io.ErrUnexpectedEOF},
		"truncated in prelude": {good[:5], io.ErrUnexpectedEOF},
		"prelude crc":          {flip(0), nil},
		"message crc":          {flip(len(good) - 1), nil},
		"payload flipped":      {flip(len(good) - 6), nil},
		"oversized headers":    {oversized, nil},
	}
	for name, c := range cases {
		_, err := readFrame(bytes.NewReader(c.data))
		if err == nil || err == io.EOF || (c.want != nil && err != c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
