package proto

import "strings"

// SanitizeTerminal makes a string safe to print to a terminal as plain text.
//
// Session metadata (names, working directories, command lines, remote hosts)
// is derived from untrusted sources — /proc/<pid>/cwd and cmdline, and the utmp
// host field (influenceable via reverse DNS). An attacker who can plant a
// file/directory name or influence those values could otherwise embed terminal
// control sequences (ESC, BEL, CSI, OSC) that execute when the operator merely
// lists or chooses sessions: window-title spoofing, clipboard writes (OSC 52),
// screen rewriting, or answerback/title-report injection that feeds the input
// buffer (command injection on some terminals).
//
// This drops C0 controls (including ESC and BEL), DEL, and C1 controls, and
// folds tab/newline/carriage-return to a single space so multi-line spoofing
// and column misalignment are not possible. Ordinary printable Unicode is kept.
func SanitizeTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case r < 0x20 || r == 0x7f: // C0 controls + DEL (covers ESC 0x1b, BEL 0x07)
			return -1
		case r >= 0x80 && r <= 0x9f: // C1 controls
			return -1
		default:
			return r
		}
	}, s)
}
