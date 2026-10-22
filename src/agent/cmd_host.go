package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// env
// ---------------------------------------------------------------------------

func (a *agent) cmdEnv(r *reader) {
	op := r.u32()
	name := r.str()
	value := r.str()
	taskID := r.u32()

	switch op {
	case 0: // list
		env := os.Environ()
		sort.Strings(env)
		a.reply(taskID, cmdEnv, func(w *writer) {
			w.u32(op)
			w.u32(uint32(len(env)))
			for _, kv := range env {
				w.str(kv)
			}
		})

	case 1: // get
		v, ok := os.LookupEnv(name)
		a.reply(taskID, cmdEnv, func(w *writer) {
			w.u32(op)
			w.u8(boolByte(ok))
			w.str(v)
		})

	case 2: // set
		err := os.Setenv(name, value)
		a.reply(taskID, cmdEnv, func(w *writer) {
			w.u32(op)
			w.u8(boolByte(err == nil))
			if err != nil {
				w.str(err.Error())
			} else {
				w.str(name + "=" + value)
			}
		})

	case 3: // unset
		err := os.Unsetenv(name)
		a.reply(taskID, cmdEnv, func(w *writer) {
			w.u32(op)
			w.u8(boolByte(err == nil))
			if err != nil {
				w.str(err.Error())
			} else {
				w.str(name)
			}
		})

	default:
		a.replyErrorCode(taskID, uint32(syscall.EINVAL))
	}
}

// ---------------------------------------------------------------------------
// uname
// ---------------------------------------------------------------------------

func (a *agent) cmdUname(r *reader) {
	taskID := r.u32()

	sysname := readTrim("/proc/sys/kernel/ostype")
	if sysname == "" {
		sysname = "Linux"
	}
	release := readTrim("/proc/sys/kernel/osrelease")
	version := readTrim("/proc/sys/kernel/version")
	node, _ := os.Hostname()

	a.reply(taskID, cmdUname, func(w *writer) {
		w.str(sysname)
		w.str(node)
		w.str(release)
		w.str(version)
		w.str(unameMachine(runtime.GOARCH))
	})
}

// ---------------------------------------------------------------------------
// sysinfo
// ---------------------------------------------------------------------------

func (a *agent) cmdSysinfo(r *reader) {
	taskID := r.u32()

	uptime := 0.0
	if s := readTrim("/proc/uptime"); s != "" {
		if f := strings.Fields(s); len(f) > 0 {
			uptime, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	load := readTrim("/proc/loadavg")
	mem := readMeminfo()

	groups := make([]string, 0, 8)
	if gl, err := os.Getgroups(); err == nil {
		for _, gid := range gl {
			groups = append(groups, groupNameByGID(gid))
		}
	}

	container := detectContainer()

	a.reply(taskID, cmdSysinfo, func(w *writer) {
		w.str(a.info.OsRelease)
		w.str(a.info.Kernel)
		w.str(a.info.Machine)
		w.str(a.info.Hostname)
		w.str(a.info.Username)
		w.u32(uint32(os.Getuid()))
		w.u32(uint32(os.Geteuid()))
		w.u32(uint32(os.Getgid()))
		w.str(strings.Join(groups, ","))
		w.u64(uint64(uptime))
		w.str(load)
		w.u64(mem["MemTotal"])
		w.u64(mem["MemFree"])
		w.u64(mem["MemAvailable"])
		w.u32(uint32(runtime.NumCPU()))
		w.str(container)
	})
}

func readMeminfo() map[string]uint64 {
	out := make(map[string]uint64)
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		fields := strings.Fields(line[idx+1:])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		out[key] = v * 1024 // values are in kB
	}
	return out
}

func detectContainer() string {
	if fileExists("/.dockerenv") {
		return "docker"
	}
	if fileExists("/run/.containerenv") {
		return "podman"
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		switch {
		case strings.Contains(s, "kubepods"):
			return "kubernetes"
		case strings.Contains(s, "docker"):
			return "docker"
		case strings.Contains(s, "lxc"):
			return "lxc"
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// service / daemon interaction
// ---------------------------------------------------------------------------

func (a *agent) cmdService(r *reader) {
	action := strings.ToLower(r.str())
	unit := r.str()
	taskID := r.u32()

	ok, output := runServiceAction(action, unit)
	a.reply(taskID, cmdService, func(w *writer) {
		w.u8(boolByte(ok))
		w.str(output)
	})
}

func runServiceAction(action, unit string) (bool, string) {
	var name string
	var args []string

	switch action {
	case "list":
		name = "systemctl"
		args = []string{"list-units", "--type=service", "--all", "--no-pager", "--no-legend"}

	case "status":
		if unit == "" {
			return false, "unit name required"
		}
		name = "systemctl"
		args = []string{"status", "--no-pager", "--lines=40", unit}

	case "start", "stop", "restart", "reload", "enable", "disable":
		if unit == "" {
			return false, "unit name required"
		}
		name = "systemctl"
		args = []string{action, unit}

	default:
		return false, "unknown action '" + action + "' (use list|status|start|stop|restart|enable|disable)"
	}

	out, err := runWithTimeout(15*time.Second, name, args...)
	if err != nil {
		// SysV fallback for hosts without systemd.
		if action != "list" {
			fallback := []string{action, unit}
			out2, err2 := runWithTimeout(15*time.Second, "service", fallback...)
			if err2 == nil {
				return true, out2
			}
		} else {
			out2, err2 := runWithTimeout(15*time.Second, "service", "--status-all")
			if err2 == nil {
				return true, out2
			}
		}
		if out != "" {
			return false, out
		}
		return false, err.Error()
	}
	return true, out
}

func runWithTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "SYSTEMD_PAGER=", "PAGER=cat")

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)

	go func() {
		out, err := cmd.CombinedOutput()
		done <- result{out, err}
	}()

	select {
	case r := <-done:
		return string(r.out), r.err
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", syscall.ETIMEDOUT
	}
}

// ---------------------------------------------------------------------------
// users (utmp)
// ---------------------------------------------------------------------------

const utmpRecordSize = 384

type utmpEntry struct {
	user  string
	line  string
	host  string
	login int64
}

func readUtmp() []utmpEntry {
	var data []byte
	for _, path := range []string{"/var/run/utmp", "/run/utmp"} {
		if b, err := os.ReadFile(path); err == nil {
			data = b
			break
		}
	}
	if data == nil {
		return nil
	}

	var out []utmpEntry
	for off := 0; off+utmpRecordSize <= len(data); off += utmpRecordSize {
		rec := data[off : off+utmpRecordSize]
		utType := int16(binary.LittleEndian.Uint16(rec[0:2]))
		if utType != 7 { // USER_PROCESS
			continue
		}
		sec := int32(binary.LittleEndian.Uint32(rec[340:344]))
		out = append(out, utmpEntry{
			line:  cstr(rec[8:40]),
			user:  cstr(rec[44:76]),
			host:  cstr(rec[76:332]),
			login: int64(sec),
		})
	}
	return out
}

func cstr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func (a *agent) cmdUsers(r *reader) {
	taskID := r.u32()
	users := readUtmp()

	a.reply(taskID, cmdUsers, func(w *writer) {
		w.u32(uint32(len(users)))
		for _, u := range users {
			w.str(u.user)
			w.str(u.line)
			w.str(u.host)
			w.u64(uint64(u.login))
		}
	})
}
