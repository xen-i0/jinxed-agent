package main

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type job struct {
	id    uint32
	jtype uint16
	pid   int

	cmd    *exec.Cmd
	master *os.File // PTY master for shell jobs
	out    io.Reader

	mu       sync.Mutex
	finished bool
	killed   bool
}

type jobManager struct {
	mu sync.Mutex
	m  map[uint32]*job
}

func newJobManager() *jobManager {
	return &jobManager{m: make(map[uint32]*job)}
}

func (jm *jobManager) add(j *job) {
	jm.mu.Lock()
	jm.m[j.id] = j
	jm.mu.Unlock()
}

func (jm *jobManager) get(id uint32) *job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.m[id]
}

func (jm *jobManager) remove(id uint32) {
	jm.mu.Lock()
	delete(jm.m, id)
	jm.mu.Unlock()
}

func (jm *jobManager) snapshot() []*job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	out := make([]*job, 0, len(jm.m))
	for _, j := range jm.m {
		out = append(out, j)
	}
	return out
}

func (jm *jobManager) killAll() {
	for _, j := range jm.snapshot() {
		killProcessGroup(j)
	}
}

func killProcessGroup(j *job) {
	if j == nil || j.cmd == nil || j.cmd.Process == nil {
		return
	}
	j.mu.Lock()
	j.killed = true
	j.mu.Unlock()

	// Every child is started with Setpgid, so the negative pid kills the whole
	// pipeline (sh -c "cmd | cmd" included).
	if err := syscall.Kill(-j.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = j.cmd.Process.Kill()
	}
}

// ---------------------------------------------------------------------------
// Output plumbing
// ---------------------------------------------------------------------------

func (a *agent) emitJobData(jobID uint32, jtype uint16, data []byte) {
	if len(data) == 0 {
		return
	}
	a.out.emit(func(w *writer) {
		w.u32(jobID)
		w.u32(cmdJob)
		w.u8(uint8(jtype))
		w.u8(jobStateRunning)
		w.str(string(data))
	})
}

// finishJob emits the terminal state record once and reaps the job.
func (a *agent) finishJob(j *job, killed bool) {
	j.mu.Lock()
	if j.finished {
		j.mu.Unlock()
		return
	}
	j.finished = true
	if j.killed {
		killed = true
	}
	j.mu.Unlock()

	state := uint8(jobStateFinished)
	if j.jtype == jobTypeShell || killed {
		// For shell jobs the extender treats KILLED as "close the terminal";
		// FINISHED would require an error code payload.
		state = jobStateKilled
	}

	a.out.emit(func(w *writer) {
		w.u32(j.id)
		w.u32(cmdJob)
		w.u8(uint8(j.jtype))
		w.u8(state)
	})

	a.jobs.remove(j.id)
	if j.master != nil {
		_ = j.master.Close()
	}
	if c, ok := j.out.(io.Closer); ok && j.master == nil {
		_ = c.Close()
	}
}

func (a *agent) pumpJob(j *job, r io.Reader) {
	buf := make([]byte, 32768)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			a.emitJobData(j.id, j.jtype, data)
		}
		if err != nil {
			break
		}
	}
	if j.cmd != nil {
		_ = j.cmd.Wait()
	}
	a.finishJob(j, false)
}

// watchJob reaps a job without an output pipe.
func (a *agent) watchJob(j *job) {
	if j.cmd == nil {
		a.finishJob(j, false)
		return
	}
	_ = j.cmd.Wait()
	a.finishJob(j, false)
}

// ---------------------------------------------------------------------------
// Process execution
// ---------------------------------------------------------------------------

// startProcess launches argv directly (no shell) in its own process group.
func (a *agent) startProcess(jobID uint32, cmdline string, wantOutput bool, jtype uint16) (int, error) {
	argv := splitCommandLine(cmdline)
	if len(argv) == 0 {
		return 0, syscall.EINVAL
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if wantOutput {
		pr, pw, err := os.Pipe()
		if err != nil {
			return 0, err
		}
		cmd.Stdout = pw
		cmd.Stderr = pw

		if err := cmd.Start(); err != nil {
			_ = pr.Close()
			_ = pw.Close()
			return 0, err
		}
		_ = pw.Close()

		j := &job{id: jobID, jtype: jtype, pid: cmd.Process.Pid, cmd: cmd, out: pr}
		a.jobs.add(j)
		go a.pumpJob(j, pr)
		return cmd.Process.Pid, nil
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	j := &job{id: jobID, jtype: jtype, pid: cmd.Process.Pid, cmd: cmd}
	a.jobs.add(j)
	go a.watchJob(j)
	return cmd.Process.Pid, nil
}

// ---------------------------------------------------------------------------
// Command handlers
// ---------------------------------------------------------------------------

func (a *agent) cmdPsRun(r *reader) {
	output := r.u8() != 0
	_ = r.u8()  // impersonation flag: not applicable on POSIX
	_ = r.u32() // program state (CREATE_SUSPENDED): not applicable
	cmdline := r.str()
	taskID := r.u32()

	pid, err := a.startProcess(taskID, cmdline, output, jobTypeProcess)
	if err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdPsRun, func(w *writer) {
		w.u32(uint32(pid))
		w.u8(boolByte(output))
		w.str(cmdline)
	})
}

func (a *agent) cmdJobsList(r *reader) {
	taskID := r.u32()
	jobs := a.jobs.snapshot()
	a.reply(taskID, cmdJobList, func(w *writer) {
		w.u32(uint32(len(jobs)))
		for _, j := range jobs {
			w.u32(j.id)
			w.u16(j.jtype)
			w.u16(uint16(j.pid))
		}
	})
}

func (a *agent) cmdJobsKill(r *reader) {
	jobID := r.u32()
	taskID := r.u32()

	j := a.jobs.get(jobID)
	if j == nil {
		a.reply(taskID, cmdJobsKill, func(w *writer) {
			w.u8(0)
			w.u32(jobID)
		})
		return
	}

	killProcessGroup(j)
	if j.jtype == jobTypeShell && j.master != nil {
		_ = j.master.Close()
		a.finishJob(j, true)
	}

	a.reply(taskID, cmdJobsKill, func(w *writer) {
		w.u8(1)
		w.u32(jobID)
	})
}

func (a *agent) cmdPsKill(r *reader) {
	pid := r.u32()
	taskID := r.u32()

	if pid == 0 || pid == uint32(os.Getpid()) {
		a.replyErrorCode(taskID, uint32(syscall.EINVAL))
		return
	}
	if err := syscall.Kill(int(pid), syscall.SIGKILL); err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdPsKill, func(w *writer) { w.u32(pid) })
}

func boolByte(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}
