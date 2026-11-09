package kiro

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

// AWS event stream framing (the binary format of generateAssistantResponse):
//
//	[total u32][headers u32][prelude crc u32][headers][payload][message crc u32]
//
// Header: name length u8, name, type u8, then a value whose size depends on the type.

const (
	maxFrameBytes  = 24 << 20
	maxHeaderBytes = 128 << 10
)

type frame struct {
	headers map[string]string // string-typed headers only (:message-type, :event-type, :exception-type)
	payload []byte
}

// readFrame reads one frame. io.EOF means the stream ended cleanly between frames; a frame cut short is
// io.ErrUnexpectedEOF; anything else is a corrupt stream (a silently truncated frame would be a truncated answer).
func readFrame(r io.Reader) (frame, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return frame{}, err
	}
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:]) {
		return frame{}, errors.New("event stream prelude CRC mismatch")
	}
	total, hlen := binary.BigEndian.Uint32(prelude[:4]), binary.BigEndian.Uint32(prelude[4:8])
	if total < 16 || total > maxFrameBytes || hlen > maxHeaderBytes || hlen > total-16 {
		return frame{}, errors.New("event stream frame bounds are invalid")
	}
	buf := make([]byte, total)
	copy(buf, prelude[:])
	if _, err := io.ReadFull(r, buf[12:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return frame{}, err
	}
	if crc32.ChecksumIEEE(buf[:total-4]) != binary.BigEndian.Uint32(buf[total-4:]) {
		return frame{}, errors.New("event stream message CRC mismatch")
	}
	h, err := parseHeaders(buf[12 : 12+hlen])
	return frame{headers: h, payload: buf[12+hlen : total-4]}, err
}

var errHeader = errors.New("event stream header exceeds its declared bounds")

func parseHeaders(b []byte) (map[string]string, error) {
	out := map[string]string{}
	for len(b) > 0 {
		n := int(b[0])
		if len(b) < 2+n {
			return nil, errHeader
		}
		name, typ := string(b[1:1+n]), b[1+n]
		b = b[2+n:]
		var size int
		switch typ {
		case 0, 1: // true, false
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8: // int64, timestamp
			size = 8
		case 9: // uuid
			size = 16
		case 6, 7: // bytes, string: u16 length first
			if len(b) < 2 {
				return nil, errHeader
			}
			size = 2 + int(binary.BigEndian.Uint16(b))
		default:
			return nil, errors.New("event stream header has an unknown type")
		}
		if len(b) < size {
			return nil, errHeader
		}
		if typ == 7 {
			out[name] = string(b[2:size])
		}
		b = b[size:]
	}
	return out, nil
}
