package sessiontitle

import (
	"testing"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

func TestFromAgent(t *testing.T) {
	tests := []struct {
		name  string
		agent proto.AgentInfo
		want  string
	}{
		{
			name:  "codex in repo",
			agent: proto.AgentInfo{Type: "codex", WorkDir: "~/projects/aethershell"},
			want:  "Aethershell/.codex",
		},
		{
			name:  "gemini keeps existing capitalization after first rune",
			agent: proto.AgentInfo{Type: "gemini", WorkDir: "/srv/AetherShell"},
			want:  "AetherShell/.gemini",
		},
		{
			name:  "home directory",
			agent: proto.AgentInfo{Type: "claude", WorkDir: "~"},
			want:  "Home/.claude",
		},
		{
			name:  "not an agent",
			agent: proto.AgentInfo{Type: "shell", WorkDir: "~/projects/aethershell"},
			want:  "",
		},
		{
			name:  "unknown agent",
			agent: proto.AgentInfo{Type: "unknown", WorkDir: "~/projects/aethershell"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromAgent(tt.agent); got != tt.want {
				t.Fatalf("FromAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFromInfoShell(t *testing.T) {
	got := FromInfo(proto.AgentInfo{Type: "shell", Command: "bash"}, "cosmo")
	want := "Cosmo:/.bash"
	if got != want {
		t.Fatalf("FromInfo() = %q, want %q", got, want)
	}
}

func TestOSC(t *testing.T) {
	got := string(OSC(" Aether\x1bShell\a/.codex\n"))
	want := "\x1b]0;AetherShell/.codex\a"
	if got != want {
		t.Fatalf("OSC() = %q, want %q", got, want)
	}
}
