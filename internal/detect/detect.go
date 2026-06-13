// Package detect identifies what coding agent (if any) is running
// inside a shell session by scanning the process tree.
package detect

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

// shellCommands are command names we consider "just a shell" and not worth
// labelling a session by.
var shellCommands = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "fish": true,
	"dash": true, "ksh": true, "tcsh": true, "csh": true,
}

// Detect scans the process tree starting from pid to determine what
// agent (or shell) is running there. If pid is 0, returns an empty shell info.
func Detect(pid int) proto.AgentInfo {
	info := proto.AgentInfo{
		Type:    "shell",
		Command: "shell",
		Title:   "shell",
	}

	if pid <= 0 {
		return info
	}

	comm := readComm(pid)
	cmdline := readCmdline(pid)
	info.Command = comm
	info.Cmdline = cmdline
	info.PID = pid
	info.WorkDir = readCWD(pid)

	// If the PID itself is a known agent, build a rich title.
	if agentType, ok := matchAgent(comm); ok {
		info.Type = agentType
		info.Command = comm
		info.Title = agentTitle(agentType, cmdline)
		return info
	}

	// If the foreground process is the shell itself, the session is idle. Do
	// not walk above it: aetherd may have been launched from another agent, and
	// that parent process must not label this shell session.
	if shellCommands[comm] {
		info.Title = "shell"
		return info
	}

	// Walk the process tree upward from pid to find an agent ancestor.
	agentType, agentComm := walkUp(pid)
	if agentType != "" {
		info.Type = agentType
		info.Command = agentComm
		info.Title = agentTitle(agentType, cmdline)
		return info
	}

	// Not an agent. Build a title from whatever the foreground process is.
	info.Title = foregroundTitle(comm, cmdline)
	return info
}

// foregroundTitle builds a display label for a non-agent foreground process.
// Examples: "vim", "python script.py", "htop", "shell" (for idle bash).
func foregroundTitle(comm, cmdline string) string {
	if shellCommands[comm] {
		return "shell"
	}
	// Use the first interesting part of the command line.
	if cmdline != "" && cmdline != comm {
		// Show the command with first arg, but keep it short.
		parts := strings.Fields(cmdline)
		if len(parts) >= 2 {
			arg := filepath.Base(parts[1])
			// Skip flag-like args (starting with -)
			for i := 1; i < len(parts) && strings.HasPrefix(arg, "-"); i++ {
				arg = filepath.Base(parts[i])
			}
			if arg != "" && !strings.HasPrefix(arg, "-") {
				return comm + " " + arg
			}
		}
	}
	return comm
}

// agentTitle builds a display label for a known agent. Uses the display name
// and appends the first meaningful argument if present (e.g., a model name).
func agentTitle(agentType, cmdline string) string {
	displayName := agentType
	if name, ok := proto.KnownAgents[agentType]; ok {
		displayName = name
	}
	if cmdline == "" {
		return displayName
	}
	// Try to extract a useful sub-command or model from the args.
	parts := strings.Fields(cmdline)
	for _, p := range parts[1:] {
		p = strings.TrimPrefix(p, "-")
		p = strings.TrimPrefix(p, "-")
		if p == "" || strings.HasPrefix(p, "-") {
			continue
		}
		// A concise positional arg adds context (e.g. "coding agent generate")
		if len(p) < 20 && !strings.Contains(p, "/") {
			return displayName + " " + p
		}
		break
	}
	return displayName
}

// readComm reads /proc/<pid>/comm to get the process name.
func readComm(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(data))
}

// readCWD reads /proc/<pid>/cwd to get the working directory.
func readCWD(pid int) string {
	link, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil {
		return "?"
	}
	// Tilde-expand home directory
	home := os.Getenv("HOME")
	if home != "" && strings.HasPrefix(link, home) {
		if link == home {
			return "~"
		}
		return "~" + link[len(home):]
	}
	return link
}

// readCmdline reads /proc/<pid>/cmdline (null-separated) and returns a
// space-joined string. The first element is the binary path; only the
// basename is kept.
func readCmdline(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(data) == 0 {
		return ""
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(parts) == 0 {
		return ""
	}
	// Replace binary path with just its basename.
	parts[0] = filepath.Base(parts[0])
	return strings.Join(parts, " ")
}

// readPPID reads /proc/<pid>/stat and returns field 4 (ppid).
func readPPID(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	stat := string(data)
	// Find closing paren of comm field
	end := strings.LastIndex(stat, ")")
	if end < 0 {
		return 0
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// walkUp walks the process tree from pid up to init, looking for known agent
// binaries. Returns (agentType, agentName).
func walkUp(pid int) (string, string) {
	visited := make(map[int]bool)
	current := pid

	for depth := 0; depth < 20 && current > 1; depth++ {
		if visited[current] {
			break
		}
		visited[current] = true

		comm := readComm(current)
		if agentType, ok := matchAgent(comm); ok {
			return agentType, comm
		}

		// Also check the full command line for agent signatures
		if agentType, agentName := matchCmdline(current); agentType != "" {
			return agentType, agentName
		}

		current = readPPID(current)
	}
	return "", ""
}

// matchAgent checks if a process name matches a known agent.
func matchAgent(comm string) (string, bool) {
	lower := strings.ToLower(comm)

	// Direct matches
	if _, ok := proto.KnownAgents[lower]; ok {
		return lower, true
	}

	// Substring matches (e.g., "claude-agent", "gemini-cli")
	for key := range proto.KnownAgents {
		if strings.Contains(lower, key) {
			return key, true
		}
	}

	// Special cases
	switch {
	case strings.Contains(lower, "node") || strings.Contains(lower, "npm"):
		// Node could be anything — don't classify without cmdline evidence
		return "", false
	case strings.Contains(lower, "python") || strings.Contains(lower, "python3"):
		return "", false
	}

	return "", false
}

// matchCmdline reads /proc/<pid>/cmdline and checks for agent signatures.
func matchCmdline(pid int) (string, string) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return "", ""
	}
	// cmdline is null-separated
	args := strings.Split(string(data), "\x00")
	full := strings.ToLower(strings.Join(args, " "))

	// Common agent CLI patterns
	patterns := map[string][]string{
		"claude":     {"claude", "claude-code", "anthropic"},
		"gemini":     {"gemini", "gemini-cli", "google-gemini"},
		"deepcode":   {"deepcode", "deep-code"},
		"codex":      {"codex", "openai-codex"},
		"perplexity": {"perplexity", "pplx"},
		"cursor":     {"cursor", "cursor-agent"},
		"copilot":    {"github-copilot", "copilot"},
		"windsurf":   {"windsurf"},
		"cody":       {"cody", "sourcegraph"},
		"aider":      {"aider"},
		"devin":      {"devin"},
		"openhands":  {"openhands"},
		"swe-agent":  {"swe-agent", "sweagent"},
	}

	for agentType, keywords := range patterns {
		for _, kw := range keywords {
			if strings.Contains(full, kw) {
				return agentType, args[0]
			}
		}
	}

	return "", ""
}
