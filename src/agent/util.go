package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// splitCommandLine tokenises a POSIX shell-ish command line without invoking a
// shell. It understands single quotes, double quotes, backslash escapes and
// runs of whitespace.
func splitCommandLine(s string) []string {
	var (
		args     []string
		cur      strings.Builder
		inSingle bool
		inDouble bool
		started  bool
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inSingle = true
			started = true
		case c == '"':
			inDouble = true
			started = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			started = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flush()
	return args
}

// errnoOf extracts a POSIX errno from an error chain, falling back to EIO.
func errnoOf(err error) int {
	if err == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return int(syscall.EIO)
}

func errnoText(code int) string {
	if code == 0 {
		return "success"
	}
	return syscall.Errno(code).Error()
}

func humanSize(n uint64) string {
	const (
		kb = 1024.0
		mb = kb * 1024
		gb = mb * 1024
		tb = gb * 1024
	)
	f := float64(n)
	switch {
	case f >= tb:
		return fmt.Sprintf("%.2f Tb", f/tb)
	case f >= gb:
		return fmt.Sprintf("%.2f Gb", f/gb)
	case f >= mb:
		return fmt.Sprintf("%.2f Mb", f/mb)
	case f >= kb:
		return fmt.Sprintf("%.2f Kb", f/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ---------------------------------------------------------------------------
// /etc/passwd lookup (the agent is built with CGO_ENABLED=0, so os/user reads
// the files directly as well; this cache keeps the process list cheap).
// ---------------------------------------------------------------------------

var (
	passwdOnce   sync.Once
	passwdByID   map[int]string
	passwdByName map[string]int
	groupOnce    sync.Once
	groupByID    map[int]string
)

func loadPasswd() {
	passwdByID = make(map[int]string)
	passwdByName = make(map[string]int)
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 4 {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		passwdByID[uid] = f[0]
		passwdByName[f[0]] = uid
	}
}

func userNameByUID(uid int) string {
	passwdOnce.Do(loadPasswd)
	if name, ok := passwdByID[uid]; ok {
		return name
	}
	return strconv.Itoa(uid)
}

func uidByName(name string) (int, bool) {
	passwdOnce.Do(loadPasswd)
	uid, ok := passwdByName[name]
	return uid, ok
}

func loadGroup() {
	groupByID = make(map[int]string)
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		gid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		groupByID[gid] = f[0]
	}
}

func groupNameByGID(gid int) string {
	groupOnce.Do(loadGroup)
	if name, ok := groupByID[gid]; ok {
		return name
	}
	return strconv.Itoa(gid)
}

// ttyName decodes the tty_nr field of /proc/<pid>/stat into a device name.
func ttyName(nr int32) string {
	if nr == 0 {
		return "?"
	}
	major := (uint32(nr) >> 8) & 0xfff
	minor := (uint32(nr) & 0xff) | ((uint32(nr) >> 12) & 0xfff00)
	switch {
	case major >= 136 && major <= 143:
		return fmt.Sprintf("pts/%d", minor)
	case major == 4:
		if minor < 64 {
			return fmt.Sprintf("tty%d", minor)
		}
		return fmt.Sprintf("ttyS%d", minor-64)
	default:
		return fmt.Sprintf("%d:%d", major, minor)
	}
}

// unescapeMount decodes the octal escapes (\040 for space) used by /proc/mounts.
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func trimNulString(b []byte) string {
	return string(trimNul(b))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
