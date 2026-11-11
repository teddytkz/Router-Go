// Package tray puts a notification-area icon with "Hide to tray" and "Exit" on Windows; elsewhere it does nothing.
package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
)

// ico wraps a PNG in an ICO container (Windows 7+ reads PNG-compressed icons), so the dashboard's own icon serves as is.
func ico(data []byte) []byte {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width > 255 || cfg.Height > 255 {
		return data // shortcut: an icon of 256px or more is stored as 0 in this header; ours is 180px
	}
	h := make([]byte, 22)
	binary.LittleEndian.PutUint16(h[2:], 1) // type: icon
	binary.LittleEndian.PutUint16(h[4:], 1) // one image
	h[6], h[7] = byte(cfg.Width), byte(cfg.Height)
	binary.LittleEndian.PutUint16(h[10:], 1) // planes
	binary.LittleEndian.PutUint16(h[12:], 32)
	binary.LittleEndian.PutUint32(h[14:], uint32(len(data)))
	binary.LittleEndian.PutUint32(h[18:], 22) // image data follows the header
	return append(h, data...)
}
