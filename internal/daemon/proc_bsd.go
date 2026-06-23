//go:build darwin || freebsd
// +build darwin freebsd

package daemon

import (
	"strconv"
	"strings"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

func procStartTicks(_ int) (uint64, bool) {
	return 0, false
}

func foregroundPID(_ int) int {
	return 0
}

func DiscoverRemoteSessions(_ map[string]bool) []proto.Session {
	return nil
}

func splitStat(stat string) []string {
	end := strings.LastIndex(stat, ")")
	if end < 0 {
		return strings.Fields(stat)
	}
	rest := strings.Fields(stat[end+2:])
	open := strings.Index(stat, "(")
	if open < 0 {
		return rest
	}
	result := make([]string, 0, 2+len(rest))
	result = append(result, strings.TrimSpace(stat[:open]))
	result = append(result, stat[open+1:end])
	result = append(result, rest...)
	return result
}

func ttyName(nr int) string {
	if nr <= 0 {
		return ""
	}
	return "tty" + strconv.Itoa(nr)
}
