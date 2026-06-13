package daemon

import (
	"bytes"
	"encoding/binary"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Gwolfgit/aethershell/internal/detect"
	"github.com/Gwolfgit/aethershell/internal/proto"
)

// External-session discovery.
//
// aether can only attach to PTYs it created. But the chooser is far more useful
// when it also *shows* the user's other live remote logins (plain SSH sessions,
// editors, other agents) so they get a single box-wide view. We discover those
// read-only by reading utmp (for remote login → tty + host) and scanning /proc
// (for what's running on each tty).
//
// Local logins (console/serial) are deliberately excluded: utmp records an empty
// host for them, and non-pts ttys decode to "". aether never touches local TTYs.

const utUserProcess = 7 // ut_type for an active user login

// utmpRecord mirrors the Linux glibc `struct utmp` (384 bytes). Field offsets
// matter — do not reorder. Explicit padding keeps Go's layout matching C's.
type utmpRecord struct {
	Type    int16
	_       [2]byte
	Pid     int32
	Line    [32]byte
	ID      [4]byte
	User    [32]byte
	Host    [256]byte
	Exit    [4]byte
	Session int32
	TvSec   int32
	TvUsec  int32
	AddrV6  [16]byte
	Unused  [20]byte
}

type remoteLogin struct {
	host string
	sec  int64
}

// parseRemoteLogins reads utmp and returns remote logins keyed by tty line
// (e.g. "pts/5"). Only USER_PROCESS entries with a non-empty host (i.e. remote)
// are returned.
func parseRemoteLogins() map[string]remoteLogin {
	out := map[string]remoteLogin{}
	f, err := os.Open("/var/run/utmp")
	if err != nil {
		return out
	}
	defer f.Close()

	for {
		var rec utmpRecord
		if err := binary.Read(f, binary.LittleEndian, &rec); err != nil {
			break
		}
		if rec.Type != utUserProcess {
			continue
		}
		host := cstr(rec.Host[:])
		if host == "" || host == ":0" {
			continue // local login — never shown
		}
		line := cstr(rec.Line[:])
		if line == "" {
			continue
		}
		out[line] = remoteLogin{host: host, sec: int64(rec.TvSec)}
	}
	return out
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// procInfo holds the /proc/<pid>/stat fields we use during discovery.
type procInfo struct {
	pid   int
	comm  string
	pgrp  int
	sid   int
	ttyNr int
	tpgid int
}

// DiscoverRemoteSessions returns read-only proto.Session entries for the
// current user's remote logins that aether does not manage. excludeTTY holds
// the pts names of aether-managed sessions (e.g. {"pts/7": true}) so they are
// not double-listed.
func DiscoverRemoteSessions(excludeTTY map[string]bool) []proto.Session {
	logins := parseRemoteLogins()
	if len(logins) == 0 {
		return nil
	}

	uid := os.Getuid()
	procs := scanUserProcs(uid)

	// Index procs by the pts name of their controlling terminal.
	byTTY := map[string][]procInfo{}
	for _, p := range procs {
		if name := ttyName(p.ttyNr); name != "" {
			byTTY[name] = append(byTTY[name], p)
		}
	}

	var out []proto.Session
	for line, lg := range logins {
		if excludeTTY[line] {
			continue
		}
		group := byTTY[line]
		if len(group) == 0 {
			continue // login recorded but no live process for this user on it
		}

		// The terminal's foreground process group (tpgid) is the same for every
		// process on the tty; take it from the first one.
		tpgid := group[0].tpgid
		fg := foregroundOnTTY(group, tpgid)
		if fg == 0 {
			continue
		}

		agent := detect.Detect(fg)
		// Skip rows whose foreground is the aether client itself (someone sitting
		// in this very chooser) — listing it would be noise/recursion.
		if agent.Command == "aether" || agent.Command == "aetherd" {
			continue
		}

		out = append(out, proto.Session{
			Name:       line,
			TTY:        line,
			RemoteHost: lg.host,
			External:   true,
			Attached:   true, // a live login is, by definition, in use
			Created:    time.Unix(lg.sec, 0),
			Agent:      agent,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// foregroundOnTTY picks the foreground process for a tty given its members and
// the terminal foreground process-group id. Prefers the group leader; this is
// robust to login→bash→agent trees where the agent is a grandchild (the old
// child-walk in foregroundPID could miss it).
func foregroundOnTTY(group []procInfo, tpgid int) int {
	if tpgid <= 0 {
		return 0
	}
	fallback := 0
	for _, p := range group {
		if p.pgrp != tpgid {
			continue
		}
		fallback = p.pid
		if p.pid == p.pgrp { // group leader — the real foreground command
			return p.pid
		}
	}
	return fallback
}

// scanUserProcs reads /proc and returns stat info for processes owned by uid
// that have a controlling terminal.
func scanUserProcs(uid int) []procInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []procInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fi, err := os.Stat("/proc/" + e.Name())
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			continue
		}
		p, ok := readProcInfo(pid)
		if !ok || p.ttyNr == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// readProcInfo parses the fields we need from /proc/<pid>/stat.
func readProcInfo(pid int) (procInfo, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	f := splitStat(string(data))
	// 0:pid 1:comm 2:state 3:ppid 4:pgrp 5:session 6:tty_nr 7:tpgid
	if len(f) < 8 {
		return procInfo{}, false
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
	return procInfo{
		pid:   atoi(f[0]),
		comm:  f[1],
		pgrp:  atoi(f[4]),
		sid:   atoi(f[5]),
		ttyNr: atoi(f[6]),
		tpgid: atoi(f[7]),
	}, true
}

// ttyName decodes a /proc stat tty_nr into a name, returning "" for anything
// that is not a UNIX98 pseudo-terminal (major 136) — i.e. local console/serial
// ttys are dropped, keeping discovery remote-only.
func ttyName(nr int) string {
	if nr == 0 {
		return ""
	}
	major := (nr >> 8) & 0xfff
	minor := (nr & 0xff) | ((nr >> 12) & 0xfff00)
	if major == 136 {
		return "pts/" + strconv.Itoa(minor)
	}
	return ""
}
