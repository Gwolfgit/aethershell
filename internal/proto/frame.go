package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

// After the attach handshake (a single newline-delimited JSON Response of type
// "attached"), the daemon and client speak a simple length-prefixed frame
// protocol over the same Unix socket. This replaces the old SCM_RIGHTS fd
// passing: the daemon stays in the data path so it can buffer scrollback and
// replay it on (re)attach.
//
// Frame layout: [1 byte type][4 byte big-endian payload length][payload...]
//
// Direction:
//
//	daemon → client: FrameData only (PTY output, including scrollback replay)
//	client → daemon: FrameData (keystrokes), FrameResize (geometry), FrameDetach
const (
	FrameData   byte = 'd' // raw PTY bytes
	FrameResize byte = 'r' // payload is a JSON-encoded Control
	FrameDetach byte = 'x' // client is leaving; no payload
)

// maxFrame bounds a single payload to guard against a corrupt/hostile length
// header. Interactive terminal writes are far smaller than this.
const maxFrame = 16 << 20

// WriteFrame writes a single framed message. It performs the header and payload
// writes back to back; callers that share a writer across goroutines must
// serialize WriteFrame calls themselves.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads one framed message. It returns the frame type and payload.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	if n == 0 {
		return hdr[0], nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}
