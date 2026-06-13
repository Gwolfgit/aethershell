package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

const restoreFDEnv = "AETHERD_RESTORE_FD"

type restoreState struct {
	SocketPath string            `json:"socket_path"`
	Order      []string          `json:"order"`
	Affinity   map[string]string `json:"affinity"`
	Sessions   []restoreSession  `json:"sessions"`
}

type restoreSession struct {
	Name        string   `json:"name"`
	CreatedUnix int64    `json:"created_unix"`
	ClientID    string   `json:"client_id,omitempty"`
	LastUnix    int64    `json:"last_unix"`
	ShellPID    int      `json:"shell_pid"`
	ShellStart  uint64   `json:"shell_start,omitempty"` // /proc starttime; PID-reuse guard
	Geometry    Geometry `json:"geometry"`
	PTYFD       int      `json:"pty_fd"`
}

// Restore builds a server from hot-upgrade metadata and inherited file
// descriptors. It is called by aetherd after exec.
func Restore(socketPath, statePath string) (*Server, net.Listener, error) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, nil, fmt.Errorf("read restore state: %w", err)
	}
	_ = os.Remove(statePath)

	var state restoreState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, nil, fmt.Errorf("parse restore state: %w", err)
	}
	if state.SocketPath != "" {
		socketPath = state.SocketPath
	}

	listenerFD, err := strconv.Atoi(os.Getenv(restoreFDEnv))
	if err != nil || listenerFD <= 0 {
		return nil, nil, fmt.Errorf("invalid %s=%q", restoreFDEnv, os.Getenv(restoreFDEnv))
	}
	listenerFile := os.NewFile(uintptr(listenerFD), "aetherd-listener")
	if listenerFile == nil {
		return nil, nil, fmt.Errorf("restore listener fd %d", listenerFD)
	}
	l, err := net.FileListener(listenerFile)
	if err != nil {
		listenerFile.Close()
		return nil, nil, fmt.Errorf("restore listener: %w", err)
	}
	listenerFile.Close()

	srv := NewServer(socketPath)
	srv.order = append([]string(nil), state.Order...)
	srv.affinity = copyStringMap(state.Affinity)

	for _, meta := range state.Sessions {
		if meta.Name == "" || meta.PTYFD <= 0 || meta.ShellPID <= 0 {
			continue
		}
		ptyFile := os.NewFile(uintptr(meta.PTYFD), "aetherd-pty-"+meta.Name)
		if ptyFile == nil {
			continue
		}
		// Re-verify the PID still belongs to the same process we snapshotted.
		// If the start-time we recorded no longer matches, the shell exited and
		// the PID was recycled — drop the session rather than adopt (and later
		// kill) an unrelated process.
		if meta.ShellStart != 0 {
			if start, ok := procStartTicks(meta.ShellPID); !ok || start != meta.ShellStart {
				ptyFile.Close()
				continue
			}
		}
		sess := &Session{
			Name:         meta.Name,
			Created:      time.Unix(0, meta.CreatedUnix),
			ClientID:     meta.ClientID,
			LastAttached: time.Unix(0, meta.LastUnix),
			pty:          ptyFile,
			shellPid:     meta.ShellPID,
			shellStart:   meta.ShellStart,
			geo:          meta.Geometry,
			hub:          newOutputHub(),
		}
		srv.sessions[sess.Name] = sess
		go sess.drain()
	}

	srv.order = compactOrder(srv.order, srv.sessions)
	return srv, l, nil
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func compactOrder(order []string, sessions map[string]*Session) []string {
	out := order[:0]
	seen := map[string]bool{}
	for _, name := range order {
		if sessions[name] == nil || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	for name := range sessions {
		if !seen[name] {
			out = append(out, name)
		}
	}
	return out
}
