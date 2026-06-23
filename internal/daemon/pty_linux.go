package daemon

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func ptySlaveName(f *os.File) string {
	n, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPTN)
	if err != nil {
		return ""
	}
	return "/dev/pts/" + fmt.Sprint(n)
}

func normalizeTTYName(path string) string {
	return strings.TrimPrefix(path, "/dev/")
}
