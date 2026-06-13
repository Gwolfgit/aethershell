package daemon

import "sync"

// scrollbackBytes is how much recent PTY output each session retains for replay
// on (re)attach. Raw terminal bytes (including escape sequences) are stored, so
// replaying them reproduces what was last on screen plus recent history.
const scrollbackBytes = 1 << 20 // 1 MiB

// replayBytes is the maximum amount of scrollback sent to a client on attach.
// The daemon still retains a larger ring, but reconnect should only redraw
// enough recent terminal state to orient the user. Replaying the whole ring can
// flood slow or freshly recovered transports and immediately disconnect them.
const replayBytes = 16 << 10 // 16 KiB

// outputHub drains a session's PTY into a bounded scrollback ring and fans the
// live byte stream out to every attached subscriber. One hub exists per session
// for the session's whole lifetime, independent of whether a client is
// attached — so background output is still captured (and the PTY still drained)
// while detached.
type outputHub struct {
	mu     sync.Mutex
	ring   []byte // bounded scrollback, oldest-first
	subs   map[*subscriber]struct{}
	closed bool
}

// subscriber is one attached client's view of the live stream. data is a
// buffered channel; if it fills (a client too slow to drain), the hub drops
// that subscriber rather than stalling the shell for everyone.
type subscriber struct {
	data chan []byte
	dead bool
}

func newOutputHub() *outputHub {
	return &outputHub{subs: make(map[*subscriber]struct{})}
}

// broadcast appends bytes to the ring and pushes them to every subscriber.
// b must not be mutated by the caller after this returns.
func (h *outputHub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.ring = append(h.ring, b...)
	if len(h.ring) > scrollbackBytes {
		// Keep the most recent scrollbackBytes. Reallocate so the backing array
		// doesn't grow without bound.
		trimmed := make([]byte, scrollbackBytes)
		copy(trimmed, h.ring[len(h.ring)-scrollbackBytes:])
		h.ring = trimmed
	}

	for sub := range h.subs {
		select {
		case sub.data <- b:
		default:
			// Slow/stuck client: drop it instead of blocking the shell.
			sub.dead = true
			close(sub.data)
			delete(h.subs, sub)
		}
	}
}

// subscribe registers a new subscriber and returns it together with a bounded
// tail snapshot to replay before live bytes. Taking the snapshot and
// registering under the same lock guarantees no gap between replayed history
// and the live stream.
func (h *outputHub) subscribe() (*subscriber, []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	snapshot := replayTail(h.ring, replayBytes)

	sub := &subscriber{data: make(chan []byte, 256)}
	if h.closed {
		close(sub.data)
		return sub, snapshot
	}
	h.subs[sub] = struct{}{}
	return sub, snapshot
}

func replayTail(b []byte, limit int) []byte {
	if limit <= 0 || len(b) == 0 {
		return nil
	}
	if len(b) <= limit {
		out := make([]byte, len(b))
		copy(out, b)
		return out
	}

	start := len(b) - limit
	// Prefer starting on a line boundary so reconnect does not begin with the
	// tail of a long command/output line when recent output has newlines.
	for i := start; i < len(b); i++ {
		if b[i] == '\n' {
			start = i + 1
			break
		}
	}
	out := make([]byte, len(b)-start)
	copy(out, b[start:])
	return out
}

// unsubscribe removes a subscriber. Safe to call more than once.
func (h *outputHub) unsubscribe(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[sub]; ok {
		delete(h.subs, sub)
		if !sub.dead {
			sub.dead = true
			close(sub.data)
		}
	}
}

// close shuts the hub down: no further broadcasts, all subscribers closed.
// Called when the PTY reaches EOF (shell exited / session killed).
func (h *outputHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for sub := range h.subs {
		if !sub.dead {
			sub.dead = true
			close(sub.data)
		}
		delete(h.subs, sub)
	}
}
