package daemon

import (
	"os"
	"strconv"
	"strings"
)

// foregroundPID reads /proc/<pid>/stat to find the foreground process group
// and then finds the process in that group with the given shell PID as ancestor.
// Returns the foreground PID, or 0 if it can't be determined.
func foregroundPID(shellPid int) int {
	if shellPid <= 0 {
		return 0
	}

	// Read the shell's stat to get tpgid (field 8, 1-indexed)
	tpgid := readTPGID(shellPid)
	if tpgid <= 0 {
		return 0
	}

	// If the foreground pgid equals the shell's own pgid, the shell itself
	// is in the foreground (idle at prompt).
	shellPgid := readPGID(shellPid)
	if tpgid == shellPgid {
		return shellPid
	}

	// Otherwise, find a child process belonging to tpgid.
	return findChildInPGID(shellPid, tpgid)
}

// readTPGID reads /proc/<pid>/stat and returns field 8 (tpgid).
func readTPGID(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// stat format: pid (comm) state ppid pgrp session tty_nr tpgid ...
	// After the comm field (which may contain spaces and parens), we split.
	fields := splitStat(string(data))
	if len(fields) < 8 {
		return 0
	}
	val, _ := strconv.Atoi(fields[7]) // 0-indexed: field 8
	return val
}

// readPGID reads /proc/<pid>/stat and returns field 5 (pgrp).
func readPGID(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	fields := splitStat(string(data))
	if len(fields) < 5 {
		return 0
	}
	val, _ := strconv.Atoi(fields[4]) // 0-indexed: field 5
	return val
}

// splitStat splits /proc/[pid]/stat, handling the comm field which is in parens
// and may itself contain spaces.
func splitStat(stat string) []string {
	// Find the closing paren of the comm field
	end := strings.LastIndex(stat, ")")
	if end < 0 {
		return strings.Fields(stat)
	}
	// Everything after ") " is space-separated fields
	rest := strings.Fields(stat[end+2:])
	// Prepend pid and comm as first two fields
	open := strings.Index(stat, "(")
	if open < 0 {
		return rest
	}
	result := make([]string, 0, 2+len(rest))
	result = append(result, stat[:open])      // pid
	result = append(result, stat[open+1:end]) // comm
	result = append(result, rest...)
	return result
}

// findChildInPGID walks /proc/<parent>/task/<parent>/children to find
// a descendant process that belongs to the given process group.
func findChildInPGID(parent int, pgid int) int {
	// Read child PIDs
	data, err := os.ReadFile("/proc/" + strconv.Itoa(parent) + "/task/" + strconv.Itoa(parent) + "/children")
	if err != nil {
		return 0
	}
	for _, field := range strings.Fields(string(data)) {
		childPid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		childPgid := readPGID(childPid)
		if childPgid == pgid {
			// Check if this child has children in the same pgid (go deeper)
			if deeper := findChildInPGID(childPid, pgid); deeper != 0 {
				return deeper
			}
			return childPid
		}
	}
	return 0
}
