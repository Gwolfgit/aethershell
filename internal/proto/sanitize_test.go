package proto

import (
	"strings"
	"testing"
)

func TestSanitizeTerminalStripsEscapeSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"osc window title", "/tmp/\x1b]2;PWNED\x1b\\evil", "/tmp/]2;PWNED\\evil"},
		{"bel", "a\x07b", "ab"},
		{"csi color", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"osc52 clipboard", "x\x1b]52;c;ZXZpbA==\x07y", "x]52;c;ZXZpbA==y"},
		{"del", "a\x7fb", "ab"},
		{"newline folds to space", "line1\nline2", "line1 line2"},
		{"tab folds to space", "a\tb", "a b"},
		{"plain unicode kept", "Pröject/.codex ✦", "Pröject/.codex ✦"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SanitizeTerminal(c.in)
			if got != c.want {
				t.Fatalf("SanitizeTerminal(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.ContainsAny(got, "\x1b\x07\x7f") {
				t.Fatalf("result still contains a control byte: %q", got)
			}
		})
	}

	// C1 controls (U+0080–U+009F), e.g. NEL (0x85) and CSI (0x9b), must also be
	// dropped — some terminals treat them as single-byte escape introducers.
	c1 := "a" + string(rune(0x85)) + "b" + string(rune(0x9b)) + "c"
	if got := SanitizeTerminal(c1); got != "abc" {
		t.Fatalf("C1 controls not stripped: got %q, want %q", got, "abc")
	}
}
