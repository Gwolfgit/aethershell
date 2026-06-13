// Package proto defines the wire protocol between aether client and aetherd daemon.
// Communication happens over a Unix domain socket using JSON messages.
// Attached sessions switch from the JSON handshake to a framed byte stream.
package proto

import "time"

// --- Client → Daemon requests ---

type Request struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`      // session name
	ClientID string `json:"client_id,omitempty"` // stable local client/window identity
	Force    bool   `json:"force,omitempty"`     // take over an in-use session
	Rows     int    `json:"rows,omitempty"`
	Cols     int    `json:"cols,omitempty"`
	XPixel   int    `json:"xpixel,omitempty"` // terminal width in pixels (0 if unknown)
	YPixel   int    `json:"ypixel,omitempty"` // terminal height in pixels (0 if unknown)
}

// --- Daemon → Client responses ---

type Response struct {
	Type     string    `json:"type"`
	Error    string    `json:"error,omitempty"`
	Sessions []Session `json:"sessions,omitempty"`
	Session  *Session  `json:"session,omitempty"`
	Rows     int       `json:"rows,omitempty"` // terminal dimensions on attach
	Cols     int       `json:"cols,omitempty"`
}

// --- Control messages (client → daemon, after attach) ---

type Control struct {
	Type   string `json:"type"` // "resize" or "detach"
	Rows   int    `json:"rows,omitempty"`
	Cols   int    `json:"cols,omitempty"`
	XPixel int    `json:"xpixel,omitempty"`
	YPixel int    `json:"ypixel,omitempty"`
}

// --- Session metadata ---

type Session struct {
	Name         string    `json:"name"`
	Created      time.Time `json:"created"`
	LastAttached time.Time `json:"last_attached,omitempty"`
	Attached     bool      `json:"attached"`
	Agent        AgentInfo `json:"agent"`

	// External marks a session that the aether daemon did NOT create — a
	// plain remote login (e.g. another SSH connection) discovered via utmp.
	// External sessions are shown read-only in the chooser: they cannot be
	// attached, killed, or restarted through aether because the daemon holds
	// no PTY master for them.
	External   bool   `json:"external,omitempty"`
	TTY        string `json:"tty,omitempty"`        // controlling terminal, e.g. "pts/5"
	RemoteHost string `json:"remotehost,omitempty"` // peer host/IP for remote logins
}

// AgentInfo describes what coding agent (or shell) is running in a session.
type AgentInfo struct {
	Type    string `json:"type"`    // "shell", "claude", "gemini", "deepcode", "codex", "cursor", "copilot", "windsurf", etc.
	Command string `json:"command"` // foreground command name (from /proc/[pid]/comm)
	Cmdline string `json:"cmdline"` // full command line (from /proc/[pid]/cmdline, nulls replaced with spaces)
	Title   string `json:"title"`   // human-readable label for the chooser: "vim", "python script.py", "coding agent", etc.
	WorkDir string `json:"workdir"` // current working directory
	PID     int    `json:"pid"`     // foreground process PID
}

// Known agent binary names that we detect in the process tree.
var KnownAgents = map[string]string{
	"claude":    "Coding Agent",
	"gemini":    "Gemini CLI",
	"deepcode":  "Deep Code",
	"codex":     "OpenAI Codex",
	"cursor":    "Cursor",
	"copilot":   "GitHub Copilot",
	"windsurf":  "Windsurf",
	"cody":      "Cody",
	"aider":     "Aider",
	"continue":  "Continue",
	"codeium":   "Codeium",
	"tabnine":   "Tabnine",
	"qoder":     "Qoder",
	"cogent":    "Cogent",
	"devin":     "Devin",
	"openhands": "OpenHands",
	"swe-agent": "SWE-Agent",
}
