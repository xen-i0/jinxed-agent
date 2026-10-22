package main

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type procInfo struct {
	pid  int
	ppid int
	tty  string
	user string
	arch string
	name string
}

func listProcesses() []procInfo {
	dir, err := os.Open("/proc")
	if err != nil {
		return nil
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil
	}

	out := make([]procInfo, 0, len(names))
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		p, ok := readProc(pid)
		if !ok {
			continue
		}
		out = append(out, p)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

func readProc(pid int) (procInfo, bool) {
	base := "/proc/" + strconv.Itoa(pid)

	raw, err := os.ReadFile(base + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	s := string(raw)
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open < 0 || closeIdx <= open {
		return procInfo{}, false
	}

	p := procInfo{pid: pid, arch: "x64"}
	comm := s[open+1 : closeIdx]
	fields := strings.Fields(s[closeIdx+1:])
	if len(fields) > 4 {
		p.ppid, _ = strconv.Atoi(fields[1])
		ttyNr, _ := strconv.Atoi(fields[4])
		p.tty = ttyName(int32(ttyNr))
	} else {
		p.tty = "?"
	}
	if p.tty == "" {
		p.tty = "?"
	}

	if n := readTrim(base + "/comm"); n != "" {
		p.name = n
	} else {
		p.name = comm
	}

	if st, err := os.Stat(base); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			p.user = userNameByUID(int(sys.Uid))
		}
	}
	if p.user == "" {
		p.user = "?"
	}

	p.arch = procArch(base)
	return p, true
}

// procArch inspects the ELF class of the process executable.
func procArch(procBase string) string {
	exe, err := os.Readlink(procBase + "/exe")
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()

	hdr := make([]byte, 5)
	if _, err := f.Read(hdr); err != nil {
		return ""
	}
	if hdr[0] != 0x7f || hdr[1] != 'E' || hdr[2] != 'L' || hdr[3] != 'F' {
		return ""
	}
	switch hdr[4] {
	case 1:
		return "x32"
	case 2:
		return "x64"
	default:
		return ""
	}
}

func (a *agent) cmdPsList(r *reader) {
	taskID := r.u32()
	procs := listProcesses()

	if len(procs) == 0 {
		a.reply(taskID, cmdPsList, func(w *writer) {
			w.u8(0)
			w.u32(uint32(syscall.ENOENT))
		})
		return
	}

	a.reply(taskID, cmdPsList, func(w *writer) {
		w.u8(1)
		w.u32(uint32(len(procs)))
		for _, p := range procs {
			w.u32(uint32(p.pid))
			w.u32(uint32(p.ppid))
			w.str(p.tty)
			w.str(p.user)
			w.str(p.arch)
			w.str(p.name)
		}
	})
}
