package client

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

// SessionChoice represents the user's selection from the chooser.
type SessionChoice struct {
	Action string // "attach", "takeover", "new", "kill", "restart", "kill_all", "restart_all", "quit"
	Name   string // session name, if applicable
}

// RunChooser displays the interactive session chooser and returns the user's choice.
// It handles keyboard navigation, session management, and confirmation prompts.
//
// Sessions are of two kinds: aether-managed (attachable, killable, restartable)
// and External (other live remote logins discovered via utmp). External rows are
// read-only — they're shown for a complete box-wide view but cannot be acted on,
// since the daemon holds no PTY master for them.
func RunChooser(sessions []proto.Session, message string) SessionChoice {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// Can't do raw mode — fall back to auto-creating a new session
		return SessionChoice{Action: "new"}
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	selected := 0
	n := len(sessions)

	hideCursor()
	defer showCursor()
	defer clearScreen()

	for {
		// Display the menu
		renderMenu(sessions, selected, message)

		key := readKey()
		switch key {
		case "UP", "k":
			if selected > 0 {
				selected--
			}
		case "DOWN", "j":
			if selected < n-1 {
				selected++
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx := int(key[0] - '1')
			if idx < n {
				selected = idx
			}
		case "ENTER":
			if n > 0 {
				s := sessions[selected]
				if s.External {
					showBar("'" + s.Name + "' is another live login (" + s.RemoteHost + ") — read-only, can't attach. (any key)")
					readKey()
					continue
				}
				if s.Attached {
					if !confirmBar("Take over in-use session '" + s.Name + "'? [y/N]") {
						continue
					}
					return SessionChoice{Action: "takeover", Name: s.Name}
				}
				return SessionChoice{Action: "attach", Name: s.Name}
			}
			continue

		case "T":
			if n > 0 {
				s := sessions[selected]
				if s.External {
					showBar("'" + s.Name + "' is read-only (external login) — can't take over. (any key)")
					readKey()
					continue
				}
				if confirmBar("Take over session '" + s.Name + "'? [y/N]") {
					return SessionChoice{Action: "takeover", Name: s.Name}
				}
			}

		case "n", "N":
			return SessionChoice{Action: "new"}

		case "t":
			if n > 0 {
				s := sessions[selected]
				if s.External {
					showBar("'" + s.Name + "' is read-only (external login) — can't terminate. (any key)")
					readKey()
					continue
				}
				if s.Attached {
					if !confirmBar("Terminate in-use session '" + s.Name + "'? [y/N]") {
						continue
					}
				}
				return SessionChoice{Action: "kill", Name: s.Name}
			}

		case "C-t":
			if managedCount(sessions) > 0 {
				if confirmBar("Terminate ALL " + strconv.Itoa(managedCount(sessions)) + " managed session(s)? [y/N]") {
					return SessionChoice{Action: "kill_all"}
				}
			}

		case "r":
			if n > 0 {
				s := sessions[selected]
				if s.External {
					showBar("'" + s.Name + "' is read-only (external login) — can't restart. (any key)")
					readKey()
					continue
				}
				if s.Attached {
					if !confirmBar("Restart in-use session '" + s.Name + "'? [y/N]") {
						continue
					}
				}
				return SessionChoice{Action: "restart", Name: s.Name}
			}

		case "C-r":
			if managedCount(sessions) > 0 {
				if confirmBar("Restart ALL " + strconv.Itoa(managedCount(sessions)) + " managed session(s)? [y/N]") {
					return SessionChoice{Action: "restart_all"}
				}
			}

		case "q", "Q", "ESC", "EOF":
			return SessionChoice{Action: "quit"}
		}
	}
}

func managedCount(sessions []proto.Session) int {
	n := 0
	for _, s := range sessions {
		if !s.External {
			n++
		}
	}
	return n
}

// ANSI helpers.
const (
	cReset  = "\x1b[0m"
	cDim    = "\x1b[2m"
	cBold   = "\x1b[1m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cCyan   = "\x1b[1;36m"
	cSelBg  = "\x1b[48;5;238m"
)

// statusCell returns the status word and its color for a session.
func statusCell(s proto.Session) (string, string) {
	switch {
	case s.External:
		return "external", cDim
	case s.Attached:
		return "in use", cYellow
	default:
		return "free", cGreen
	}
}

// renderMenu draws the full-screen session chooser.
func renderMenu(sessions []proto.Session, selected int, message string) {
	// term.GetSize returns (width, height) == (cols, rows).
	cols, rows, _ := term.GetSize(int(os.Stdin.Fd()))
	if rows < 10 {
		rows = 24
	}
	if cols < 50 {
		cols = 80
	}

	// Move to top-left and clear.
	fmt.Print("\x1b[H\x1b[2J")

	host, _ := os.Hostname()
	managed := managedCount(sessions)
	external := len(sessions) - managed

	// Title.
	fmt.Printf("\r\n  %s✦ aethershell%s  %s· sessions on %s%s\r\n\r\n",
		cCyan, cReset, cDim, host, cReset)

	if message != "" {
		fmt.Printf("  %s%s%s\r\n\r\n", cYellow, padOrTrunc(message, cols-4), cReset)
	}

	// Column header.
	hdr := rowLine("", "#", "NAME / TTY", "STATUS", "RUNNING", "DIR", "AGE", "HOST")
	fmt.Printf("%s%s%s\r\n", cDim, runePadTrunc(hdr, cols), cReset)

	n := len(sessions)
	if n == 0 {
		fmt.Printf("\r\n    %s(no sessions — press n to start one)%s\r\n", cDim, cReset)
	} else {
		bodyRows := rows - 9
		if message != "" {
			bodyRows -= 2
		}
		if bodyRows < 1 {
			bodyRows = 1
		}
		start := 0
		if selected >= bodyRows {
			start = selected - bodyRows + 1
		}
		end := start + bodyRows
		if end > n {
			end = n
		}

		for i := start; i < end; i++ {
			s := sessions[i]
			stext, scolor := statusCell(s)
			marker := "●"
			if s.External {
				marker = "○"
			}
			arrow := " "
			if i == selected {
				arrow = "▸"
			}
			running := agentTitle(s)
			line := rowLine(arrow, strconv.Itoa(i+1)+" "+marker,
				trunc(s.Name, 22), stext, trunc(running, 26),
				trunc(s.Agent.WorkDir, 18), sessionAge(s.Created), s.RemoteHost)
			line = runePadTrunc(line, cols)

			if i == selected {
				fmt.Printf("%s%s%s%s%s\r\n", cSelBg, cBold, scolor, line, cReset)
			} else {
				fmt.Printf("%s%s%s\r\n", scolor, line, cReset)
			}
		}
	}

	// Bottom status bars.
	barA := fmt.Sprintf(" ↑/↓ move · ⏎ attach/take over · n new · t kill · r restart")
	barB := fmt.Sprintf(" ^t kill all · ^r restart all · q quit      %d managed · %d external", managed, external)
	barA = runePadTrunc(barA, cols)
	barB = runePadTrunc(barB, cols)
	fmt.Printf("\x1b[%d;1H\x1b[7m%s\x1b[0m", rows-1, barA)
	fmt.Printf("\x1b[%d;1H\x1b[7m%s\x1b[0m", rows, barB)
}

// rowLine lays out the fixed-width session columns (no color). Used for both
// the header and each row so they stay aligned.
func rowLine(arrow, id, name, status, running, dir, age, host string) string {
	return fmt.Sprintf(" %-1s %-4s %-22s %-8s %-26s %-18s %-4s %s",
		arrow, id, name, status, running, dir, age, host)
}

// agentTitle formats the "running" column for a session.
func agentTitle(s proto.Session) string {
	if s.Agent.Title != "" && s.Agent.Title != "shell" {
		return s.Agent.Title
	}
	return "shell"
}

// runePadTrunc pads or truncates s to exactly width display columns, counting
// runes (the box/marker glyphs used here are all single-width).
func runePadTrunc(s string, width int) string {
	if width < 0 {
		width = 0
	}
	r := []rune(s)
	if len(r) > width {
		return string(r[:width])
	}
	return s + strings.Repeat(" ", width-len(r))
}

// --- Raw terminal input helpers ---

func readKey() string {
	var buf [16]byte
	n, err := os.Stdin.Read(buf[:])
	if err != nil || n == 0 {
		return "EOF"
	}

	switch buf[0] {
	case '\r', '\n':
		return "ENTER"
	case '\x1b':
		if n == 1 {
			return "ESC"
		}
		if n >= 3 && buf[1] == '[' {
			switch buf[2] {
			case 'A':
				return "UP"
			case 'B':
				return "DOWN"
			case 'C':
				return "ENTER" // right arrow acts as enter
			case 'D':
				return "UP" // left arrow acts as up
			}
		}
		return "ESC"
	case '\x14': // Ctrl-T
		return "C-t"
	case '\x12': // Ctrl-R
		return "C-r"
	default:
		// ASCII digit 1-9
		if buf[0] >= '1' && buf[0] <= '9' {
			return string(buf[0])
		}
		return string(buf[0])
	}
}

func hideCursor()  { fmt.Print("\x1b[?25l") }
func showCursor()  { fmt.Print("\x1b[?25h\x1b[0m") }
func clearScreen() { fmt.Print("\x1b[2J\x1b[H") }

func showBar(msg string) {
	// term.GetSize returns (width, height) == (cols, rows).
	cols, rows, _ := term.GetSize(int(os.Stdin.Fd()))
	if rows < 1 {
		rows = 24
	}
	if cols < 20 {
		cols = 80
	}
	fmt.Printf("\x1b[%d;1H\x1b[7m%s\x1b[0m", rows, padOrTrunc(msg, cols))
}

func confirmBar(msg string) bool {
	showBar(msg)
	key := readKey()
	return key == "y" || key == "Y"
}

func padOrTrunc(s string, width int) string {
	if len(s) > width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

func trunc(s string, width int) string {
	if len(s) <= width {
		return s
	}
	if width <= 1 {
		return s[:width]
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}

func sessionAge(created time.Time) string {
	if created.IsZero() {
		return "-"
	}
	d := time.Since(created).Round(time.Second)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}
