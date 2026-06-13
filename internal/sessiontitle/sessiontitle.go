package sessiontitle

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

var shellCommands = map[string]bool{
	"bash": true,
	"sh":   true,
	"zsh":  true,
	"fish": true,
	"dash": true,
	"ksh":  true,
	"tcsh": true,
	"csh":  true,
}

// FromInfo returns the tab/session title for a foreground process. Recognized
// coding agents use "<cwd segment>/.<agent>", for example "Aethershell/.codex".
// Idle shells use "<host>:/.<shell>", for example "Cosmo:/.bash".
func FromInfo(agent proto.AgentInfo, hostname string) string {
	if title := FromAgent(agent); title != "" {
		return title
	}
	return FromShell(agent, hostname)
}

// FromAgent returns the tab/session title for a recognized coding agent.
func FromAgent(agent proto.AgentInfo) string {
	agentName := strings.ToLower(strings.TrimSpace(agent.Type))
	if agentName == "" || agentName == "shell" {
		return ""
	}
	if _, ok := proto.KnownAgents[agentName]; !ok {
		return ""
	}

	segment := cwdSegment(agent.WorkDir)
	if segment == "" {
		return ""
	}
	return segment + "/." + agentName
}

// FromShell returns the tab/session title for an idle foreground shell.
func FromShell(agent proto.AgentInfo, hostname string) string {
	if strings.ToLower(strings.TrimSpace(agent.Type)) != "shell" {
		return ""
	}
	shell := strings.ToLower(strings.TrimSpace(agent.Command))
	if !shellCommands[shell] {
		return ""
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return ""
	}
	return capitalizeFirst(hostname) + ":/." + shell
}

func cwdSegment(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" || workDir == "?" {
		return ""
	}
	workDir = strings.TrimRight(workDir, "/")
	if workDir == "" {
		return ""
	}
	if workDir == "~" {
		return "Home"
	}

	segment := filepath.Base(workDir)
	if segment == "." || segment == "/" || segment == "" {
		return ""
	}
	return capitalizeFirst(segment)
}

func capitalizeFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// OSC returns a terminal title escape sequence for title. It uses OSC 0 so both
// the icon/window title and tab title are updated in VTE-based terminals.
func OSC(title string) []byte {
	title = sanitize(title)
	if title == "" {
		return nil
	}
	return []byte("\x1b]0;" + title + "\a")
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\x1b' || r == '\a':
			return -1
		case r < 0x20 || r == 0x7f:
			return ' '
		default:
			return r
		}
	}, strings.TrimSpace(s))
}
