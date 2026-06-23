//go:build darwin || freebsd
// +build darwin freebsd

package daemon

import (
	"os"
	"strings"
)

func ptySlaveName(_ *os.File) string {
	return ""
}

func normalizeTTYName(path string) string {
	return strings.TrimPrefix(path, "/dev/")
}
