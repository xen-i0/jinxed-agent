package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Adaptix-Framework/axc2"
)

// ProcessData parses one decrypted response stream produced by the Linux agent
// and turns every record into a teamserver task update / GUI refresh.
func (ext *ExtenderAgent) ProcessData(agentData adaptix.AgentData, decryptedData []byte) error {
	var outTasks []adaptix.TaskData

	baseTask := adaptix.TaskData{
		Type:        adaptix.TASK_TYPE_TASK,
		AgentId:     agentData.Id,
		FinishDate:  time.Now().Unix(),
		MessageType: adaptix.MESSAGE_SUCCESS,
		Completed:   true,
		Sync:        true,
	}

	packer := CreatePacker(decryptedData)
	if !packer.CheckPacker([]string{"int"}) {
		return errors.New("failed to unmarshal message")
	}
	size := packer.ParseInt32()
	if int(size)-4 != int(packer.Size()) {
		return errors.New("failed to unmarshal message")
	}

	for packer.Size() >= 8 {
		if !packer.CheckPacker([]string{"int", "int"}) {
			break
		}

		TaskId := packer.ParseInt32()
		commandId := packer.ParseInt32()

		task := baseTask
		task.TaskId = fmt.Sprintf("%08x", TaskId)

		switch commandId {

		case COMMAND_CAT:
			if !packer.CheckPacker([]string{"array", "array"}) {
				goto DONE
			}
			path := packer.ParseString()
			content := packer.ParseBytes()
			task.Message = fmt.Sprintf("'%v' file content:", path)
			task.ClearText = string(content)

		case COMMAND_CD:
			if !packer.CheckPacker([]string{"array"}) {
				goto DONE
			}
			task.Message = "Current working directory:"
			task.ClearText = packer.ParseString()

		case COMMAND_COPY:
			task.Message = "File copied successfully"

		case COMMAND_DISKS:
			if !packer.CheckPacker([]string{"byte"}) {
				goto DONE
			}
			result := packer.ParseInt8()
			var drives []adaptix.ListingDrivesDataWin

			if result == 0 {
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				code := packer.ParseInt32()
				task.Message = fmt.Sprintf("Error [%d]: %s", code, posixErrString(int(code)))
				task.MessageType = adaptix.MESSAGE_ERROR
			} else {
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				count := int(packer.ParseInt32())
				var text strings.Builder
				text.WriteString(fmt.Sprintf(" %-24s %-14s %-12s %-12s %s\n", "Mount", "Type", "Size", "Free", "Used"))
				text.WriteString(fmt.Sprintf(" %-24s %-14s %-12s %-12s %s", "-----", "----", "----", "----", "----"))

				for i := 0; i < count; i++ {
					if !packer.CheckPacker([]string{"array", "array", "long", "long"}) {
						goto DONE
					}
					mount := packer.ParseString()
					fstype := packer.ParseString()
					total := packer.ParseInt64()
					free := packer.ParseInt64()

					used := uint64(0)
					usedPct := 0
					if total > 0 {
						used = total - free
						usedPct = int(used * 100 / total)
					}
					drives = append(drives, adaptix.ListingDrivesDataWin{
						Name: mount,
						Type: fmt.Sprintf("%s (%s)", fstype, sizeBytesToFormat(int64(total))),
					})
					text.WriteString(fmt.Sprintf("\n %-24s %-14s %-12s %-12s %d%%",
						mount, fstype, sizeBytesToFormat(int64(total)), sizeBytesToFormat(int64(free)), usedPct))
				}
				task.Message = "List of mounted filesystems:"
				task.ClearText = text.String()
			}
			Ts.TsClientGuiDisksWindows(task, drives)

		case COMMAND_DOWNLOAD:
			if !packer.CheckPacker([]string{"int", "byte"}) {
				goto DONE
			}
			fileId := fmt.Sprintf("%08x", packer.ParseInt32())
			downloadCommand := packer.ParseInt8()

			switch downloadCommand {
			case DOWNLOAD_START:
				if !packer.CheckPacker([]string{"long", "array"}) {
					goto DONE
				}
				fileSize := packer.ParseInt64()
				fileName := packer.ParseString()
				task.Message = fmt.Sprintf("The download of the '%s' file (%v bytes) has started: [fid %v]", fileName, fileSize, fileId)
				task.Completed = false
				_ = Ts.TsDownloadAdd(agentData.Id, fileId, fileName, int64(fileSize))

			case DOWNLOAD_CONTINUE:
				if !packer.CheckPacker([]string{"array"}) {
					goto DONE
				}
				content := packer.ParseBytes()
				task.Completed = false
				_ = Ts.TsDownloadUpdate(fileId, adaptix.DOWNLOAD_STATE_RUNNING, content)
				continue

			case DOWNLOAD_FINISH:
				task.Message = fmt.Sprintf("File download complete: [fid %v]", fileId)
				_ = Ts.TsDownloadClose(fileId, adaptix.DOWNLOAD_STATE_FINISHED)
			}

		case COMMAND_EXFIL:
			if !packer.CheckPacker([]string{"int", "byte"}) {
				goto DONE
			}
			fileId := fmt.Sprintf("%08x", packer.ParseInt32())
			state := packer.ParseInt8()

			switch state {
			case adaptix.DOWNLOAD_STATE_STOPPED:
				task.Message = fmt.Sprintf("Download '%v' successfully stopped", fileId)
				_ = Ts.TsDownloadUpdate(fileId, adaptix.DOWNLOAD_STATE_STOPPED, []byte(""))
			case adaptix.DOWNLOAD_STATE_RUNNING:
				task.Message = fmt.Sprintf("Download '%v' successfully resumed", fileId)
				_ = Ts.TsDownloadUpdate(fileId, adaptix.DOWNLOAD_STATE_RUNNING, []byte(""))
			case adaptix.DOWNLOAD_STATE_CANCELED:
				task.Message = fmt.Sprintf("Download '%v' successfully canceled", fileId)
				_ = Ts.TsDownloadClose(fileId, adaptix.DOWNLOAD_STATE_CANCELED)
			}

		case COMMAND_GETUID:
			if !packer.CheckPacker([]string{"byte", "array", "array"}) {
				goto DONE
			}
			high := packer.ParseInt8()
			domain := packer.ParseString()
			username := packer.ParseString()

			message := ""
			if username != "" {
				if domain != "" {
					username = domain + "\\" + username
				}
				message = fmt.Sprintf("You are '%v'", username)
				if high > 0 {
					message += " (elevated)"
				}
			}
			task.Message = message

		case COMMAND_JOB:
			if !packer.CheckPacker([]string{"byte", "byte"}) {
				goto DONE
			}
			jobType := packer.ParseInt8()
			state := packer.ParseInt8()

			if jobType == JOB_TYPE_SHELL {
				terminalId := task.TaskId
				switch state {
				case JOB_STATE_STARTING:
					Ts.TsTerminalConnResume(agentData.Id, terminalId, false)
				case JOB_STATE_RUNNING:
					if !packer.CheckPacker([]string{"array"}) {
						goto DONE
					}
					data := packer.ParseString()
					Ts.TsTerminalConnData(terminalId, []byte(data))
				case JOB_STATE_KILLED:
					_ = Ts.TsTerminalConnClose(terminalId, "Terminal stopped")
				case JOB_STATE_FINISHED:
					if !packer.CheckPacker([]string{"int"}) {
						goto DONE
					}
					code := packer.ParseInt32()
					status := fmt.Sprintf("Error [%d]: %s", code, posixErrString(int(code)))
					_ = Ts.TsTerminalConnClose(terminalId, status)
				}
			} else {
				switch state {
				case JOB_STATE_RUNNING:
					if !packer.CheckPacker([]string{"array"}) {
						goto DONE
					}
					task.Completed = false
					task.Message = fmt.Sprintf("Job [%v] output:", task.TaskId)
					task.ClearText = packer.ParseString()
				case JOB_STATE_KILLED:
					task.MessageType = adaptix.MESSAGE_INFO
					task.Message = fmt.Sprintf("Job [%v] canceled", task.TaskId)
				case JOB_STATE_FINISHED:
					task.Message = fmt.Sprintf("Job [%v] finished", task.TaskId)
				}
			}

		case COMMAND_JOB_LIST:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			count := int(packer.ParseInt32())
			if count > 0 {
				var out strings.Builder
				out.WriteString(fmt.Sprintf(" %-10s  %-6s  %-13s\n", "JobID", "PID", "Type"))
				out.WriteString(fmt.Sprintf(" %-10s  %-6s  %-13s", "--------", "-----", "-------"))
				for i := 0; i < count; i++ {
					if !packer.CheckPacker([]string{"int", "word", "word"}) {
						goto DONE
					}
					jobId := fmt.Sprintf("%08x", packer.ParseInt32())
					jobType := packer.ParseInt16()
					pid := packer.ParseInt16()

					stringType := "Unknown"
					switch jobType {
					case JOB_TYPE_LOCAL:
						stringType = "Local"
					case JOB_TYPE_REMOTE:
						stringType = "Remote"
					case JOB_TYPE_PROCESS:
						stringType = "Process"
					case JOB_TYPE_SHELL:
						stringType = "Shell"
					}
					out.WriteString(fmt.Sprintf("\n %-10v  %-6v  %-13s", jobId, pid, stringType))
				}
				task.Message = "Job list:"
				task.ClearText = out.String()
			} else {
				task.Message = "No active jobs"
			}

		case COMMAND_JOBS_KILL:
			if !packer.CheckPacker([]string{"byte", "int"}) {
				goto DONE
			}
			result := packer.ParseInt8()
			jobId := packer.ParseInt32()
			if result == 0 {
				task.MessageType = adaptix.MESSAGE_ERROR
				task.Message = fmt.Sprintf("Job %v not found", jobId)
			} else {
				task.Message = fmt.Sprintf("Job %v marked as killed", jobId)
			}

		case COMMAND_LS:
			if !packer.CheckPacker([]string{"byte"}) {
				goto DONE
			}
			result := packer.ParseInt8()
			var items []adaptix.ListingFileDataUnix
			rootPath := ""

			if result == 0 {
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				code := packer.ParseInt32()
				task.Message = fmt.Sprintf("Error [%d]: %s", code, posixErrString(int(code)))
				task.MessageType = adaptix.MESSAGE_ERROR
			} else {
				if !packer.CheckPacker([]string{"array", "int"}) {
					goto DONE
				}
				rootPath = packer.ParseString()
				count := int(packer.ParseInt32())

				if count == 0 {
					task.Message = fmt.Sprintf("The '%s' directory is EMPTY", rootPath)
				} else {
					var folders, files []adaptix.ListingFileDataUnix

					for i := 0; i < count; i++ {
						if !packer.CheckPacker([]string{"byte", "long", "int", "int", "array", "array", "array"}) {
							goto DONE
						}
						isDir := packer.ParseInt8() > 0
						size := int64(packer.ParseInt64())
						date := int64(packer.ParseInt32())
						mode := uint32(packer.ParseInt32())
						user := packer.ParseString()
						group := packer.ParseString()
						name := packer.ParseString()

						row := adaptix.ListingFileDataUnix{
							IsDir:    isDir,
							Mode:     osFileModeString(mode),
							User:     user,
							Group:    group,
							Size:     size,
							Date:     formatUnixTime(date),
							Filename: name,
						}
						if isDir {
							folders = append(folders, row)
						} else {
							files = append(files, row)
						}
					}
					items = append(folders, files...)

					var out strings.Builder
					out.WriteString(fmt.Sprintf(" %-11s %-8s %-9s %-9s %-10s %-17s %s\n",
						"Mode", "Links", "User", "Group", "Size", "Last Modified", "Name"))
					out.WriteString(fmt.Sprintf(" %-11s %-8s %-9s %-9s %-10s %-17s %s",
						"----", "-----", "----", "-----", "----", "-------------", "----"))
					for _, it := range items {
						sizeText := sizeBytesToFormat(it.Size)
						if it.IsDir {
							sizeText = ""
						}
						out.WriteString(fmt.Sprintf("\n %-11s %-8s %-9s %-9s %-10s %-17s %s",
							it.Mode, "", it.User, it.Group, sizeText, it.Date, it.Filename))
					}
					task.Message = fmt.Sprintf("Listing '%s'", rootPath)
					task.ClearText = out.String()
				}
			}
			Ts.TsClientGuiFilesUnix(task, rootPath, items)

		case COMMAND_MKDIR:
			if !packer.CheckPacker([]string{"array"}) {
				goto DONE
			}
			task.Message = fmt.Sprintf("Directory '%v' created successfully", packer.ParseString())

		case COMMAND_MV:
			task.Message = "File moved successfully"

		case COMMAND_PROFILE:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			subcommand := packer.ParseInt32()
			switch subcommand {
			case 1:
				if !packer.CheckPacker([]string{"int", "int"}) {
					goto DONE
				}
				agentData.Sleep = packer.ParseInt32()
				agentData.Jitter = packer.ParseInt32()
				task.Message = "Sleep time has been changed"
				_ = Ts.TsAgentUpdateData(agentData)
			case 2:
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				task.Message = fmt.Sprintf("Download chunk size set to %v bytes", packer.ParseInt32())
			case 3:
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				agentData.KillDate = int(packer.ParseInt32())
				task.Message = "Option 'killdate' is set"
				if agentData.KillDate == 0 {
					task.Message = "The 'killdate' option is disabled"
				}
				_ = Ts.TsAgentUpdateData(agentData)
			case 4:
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				agentData.WorkingTime = int(packer.ParseInt32())
				task.Message = "Option 'workingtime' is set"
				if agentData.WorkingTime == 0 {
					task.Message = "The 'workingtime' option is disabled"
				}
				_ = Ts.TsAgentUpdateData(agentData)
			case 5, 6:
				if !packer.CheckPacker([]string{"int", "int", "int"}) {
					goto DONE
				}
				_ = packer.ParseInt32()
				_ = packer.ParseInt32()
				_ = packer.ParseInt32()
				task.Message = "Burst mode is not used by the HTTP transport"
			}

		case COMMAND_PS_LIST:
			if !packer.CheckPacker([]string{"byte"}) {
				goto DONE
			}
			result := packer.ParseInt8()
			var procs []adaptix.ListingProcessDataUnix

			if result == 0 {
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				code := packer.ParseInt32()
				task.Message = fmt.Sprintf("Error [%d]: %s", code, posixErrString(int(code)))
				task.MessageType = adaptix.MESSAGE_ERROR
			} else {
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				count := int(packer.ParseInt32())
				if count == 0 {
					task.Message = "Failed to get process list"
					task.MessageType = adaptix.MESSAGE_ERROR
					break
				}

				contextWidth := 10
				type row struct {
					pid, ppid uint
					tty       string
					context   string
					arch      string
					name      string
				}
				var rows []row

				for i := 0; i < count; i++ {
					if !packer.CheckPacker([]string{"int", "int", "array", "array", "array", "array"}) {
						goto DONE
					}
					pid := uint(packer.ParseInt32())
					ppid := uint(packer.ParseInt32())
					tty := packer.ParseString()
					user := packer.ParseString()
					arch := packer.ParseString()
					name := packer.ParseString()

					context := user
					if arch != "" {
						context = user + " (" + arch + ")"
					}
					if len(context) > contextWidth {
						contextWidth = len(context)
					}

					procs = append(procs, adaptix.ListingProcessDataUnix{
						Pid:         pid,
						Ppid:        ppid,
						TTY:         tty,
						Context:     context,
						ProcessName: name,
					})
					rows = append(rows, row{pid, ppid, tty, context, arch, name})
				}

				sort.Slice(rows, func(i, j int) bool { return rows[i].pid < rows[j].pid })

				format := fmt.Sprintf(" %%-6v %%-6v %%-8v %%-%dv %%v", contextWidth)
				var out strings.Builder
				out.WriteString(fmt.Sprintf(format, "PID", "PPID", "TTY", "Context", "Process"))
				out.WriteString(fmt.Sprintf("\n"+format, "---", "----", "---", "-------", "-------"))
				for _, r := range rows {
					out.WriteString(fmt.Sprintf("\n"+format, r.pid, r.ppid, r.tty, r.context, r.name))
				}
				task.Message = "Process list:"
				task.ClearText = out.String()
			}
			Ts.TsClientGuiProcessUnix(task, procs)

		case COMMAND_PS_KILL:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			task.Message = fmt.Sprintf("Process %d killed", packer.ParseInt32())

		case COMMAND_PS_RUN:
			if !packer.CheckPacker([]string{"int", "byte", "array"}) {
				goto DONE
			}
			pid := packer.ParseInt32()
			isOutput := packer.ParseInt8()
			prog := packer.ParseString()

			status := "no output"
			if isOutput > 0 {
				status = "with output"
			}
			task.Completed = false
			task.Message = fmt.Sprintf("Program %v started with PID %d (output - %v)", prog, pid, status)

		case COMMAND_PWD:
			if !packer.CheckPacker([]string{"array"}) {
				goto DONE
			}
			task.Message = "Current working directory:"
			task.ClearText = packer.ParseString()

		case COMMAND_RM:
			if !packer.CheckPacker([]string{"byte"}) {
				goto DONE
			}
			if packer.ParseInt8() == 0 {
				task.Message = "File deleted successfully"
			} else {
				task.Message = "Directory deleted successfully"
			}

		case COMMAND_TUNNEL_START_TCP:
			if !packer.CheckPacker([]string{"int", "int"}) {
				goto DONE
			}
			channelId := int(TaskId)
			_ = packer.ParseInt32() // tunnel type
			result := packer.ParseInt32()
			switch result {
			case 0:
				Ts.TsTunnelConnectionResume(agentData.Id, channelId, false)
			case 1:
				Ts.TsTunnelConnectionClose(channelId, true)
			default:
				// result carries a POSIX errno; map the two common cases to the
				// SOCKS5 reply codes the client understands.
				errorCode := adaptix.SOCKS5_HOST_UNREACHABLE
				if result == uint(111) { // ECONNREFUSED
					errorCode = adaptix.SOCKS5_CONNECTION_REFUSED
				} else if result == uint(101) { // ENETUNREACH
					errorCode = adaptix.SOCKS5_NETWORK_UNREACHABLE
				}
				Ts.TsTunnelConnectionHalt(channelId, errorCode)
			}

		case COMMAND_TUNNEL_WRITE_TCP:
			if !packer.CheckPacker([]string{"array"}) {
				goto DONE
			}
			channelId := int(TaskId)
			Ts.TsTunnelConnectionData(channelId, packer.ParseBytes())

		case COMMAND_TUNNEL_REVERSE:
			if !packer.CheckPacker([]string{"int", "int"}) {
				goto DONE
			}
			tunnelId := int(TaskId)
			_ = packer.ParseInt32()
			result := packer.ParseInt32()

			var err error
			task.TaskId, task.Message, err = Ts.TsTunnelUpdateRportfwd(tunnelId, result != 0)
			if err != nil {
				task.MessageType = adaptix.MESSAGE_ERROR
			} else {
				task.MessageType = adaptix.MESSAGE_SUCCESS
			}

		case COMMAND_TUNNEL_ACCEPT:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			tunnelId := int(TaskId)
			channelId := int(packer.ParseInt32())
			Ts.TsTunnelConnectionAccept(tunnelId, channelId)

		case COMMAND_TUNNEL_PAUSE:
			Ts.TsTunnelPause(int(TaskId))

		case COMMAND_TUNNEL_RESUME:
			Ts.TsTunnelResume(int(TaskId))

		case COMMAND_TUNNEL_CLOSE:
			if !packer.CheckPacker([]string{"int", "int"}) {
				goto DONE
			}
			_ = packer.ParseInt32()
			_ = packer.ParseInt32()
			Ts.TsTunnelConnectionClose(int(TaskId), false)

		case COMMAND_TERMINATE:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			exitMethod := packer.ParseInt32()
			if exitMethod == 1 {
				task.Message = "The agent has completed its work (graceful thread exit)"
			} else if exitMethod == 2 {
				task.Message = "The agent has completed its work (kill process)"
			}
			_ = Ts.TsAgentTerminate(agentData.Id, task.TaskId)

		case COMMAND_UPLOAD:
			task.Message = "File successfully uploaded"
			Ts.TsClientGuiFilesStatus(task)

		case COMMAND_ERROR:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			code := packer.ParseInt32()
			task.MessageType = adaptix.MESSAGE_ERROR
			task.Message = fmt.Sprintf("Error [%d]: %s", code, posixErrString(int(code)))

		// -----------------------------------------------------------------
		// Linux extensions
		// -----------------------------------------------------------------

		case COMMAND_ENV:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			op := packer.ParseInt32()
			switch op {
			case 0:
				if !packer.CheckPacker([]string{"int"}) {
					goto DONE
				}
				count := int(packer.ParseInt32())
				var out strings.Builder
				for i := 0; i < count; i++ {
					if !packer.CheckPacker([]string{"array"}) {
						goto DONE
					}
					if i > 0 {
						out.WriteString("\n")
					}
					out.WriteString(packer.ParseString())
				}
				task.Message = fmt.Sprintf("Environment (%d variables):", count)
				task.ClearText = out.String()
			case 1:
				if !packer.CheckPacker([]string{"byte", "array"}) {
					goto DONE
				}
				found := packer.ParseInt8()
				value := packer.ParseString()
				if found == 0 {
					task.MessageType = adaptix.MESSAGE_ERROR
					task.Message = "Variable is not set"
				} else {
					task.Message = "Variable value:"
					task.ClearText = value
				}
			case 2, 3:
				if !packer.CheckPacker([]string{"byte", "array"}) {
					goto DONE
				}
				ok := packer.ParseInt8()
				detail := packer.ParseString()
				if ok == 0 {
					task.MessageType = adaptix.MESSAGE_ERROR
					task.Message = "Environment change failed: " + detail
				} else {
					task.Message = "Environment updated: " + detail
				}
			}

		case COMMAND_NETSTAT:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			count := int(packer.ParseInt32())
			var out strings.Builder
			out.WriteString(fmt.Sprintf(" %-6s %-24s %-24s %-12s %-6s %s\n",
				"Proto", "Local", "Remote", "State", "UID", "Inode"))
			out.WriteString(fmt.Sprintf(" %-6s %-24s %-24s %-12s %-6s %s",
				"-----", "-----", "------", "-----", "---", "-----"))
			for i := 0; i < count; i++ {
				if !packer.CheckPacker([]string{"array", "array", "array", "array", "int", "long"}) {
					goto DONE
				}
				proto := packer.ParseString()
				local := packer.ParseString()
				remote := packer.ParseString()
				state := packer.ParseString()
				uid := packer.ParseInt32()
				inode := packer.ParseInt64()
				out.WriteString(fmt.Sprintf("\n %-6s %-24s %-24s %-12s %-6d %d",
					proto, local, remote, state, uid, inode))
			}
			task.Message = fmt.Sprintf("Sockets (%d):", count)
			task.ClearText = out.String()

		case COMMAND_IFCONFIG:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			count := int(packer.ParseInt32())
			var out strings.Builder
			for i := 0; i < count; i++ {
				if !packer.CheckPacker([]string{"array", "array", "int", "array", "array"}) {
					goto DONE
				}
				name := packer.ParseString()
				addrs := packer.ParseString()
				mtu := packer.ParseInt32()
				flags := packer.ParseString()
				hw := packer.ParseString()

				if i > 0 {
					out.WriteString("\n")
				}
				out.WriteString(fmt.Sprintf("%s: flags=%s  mtu %d\n", name, flags, mtu))
				out.WriteString(fmt.Sprintf("    hwaddr %s\n", hw))
				out.WriteString(fmt.Sprintf("    inet %s", addrs))
			}
			if count == 0 {
				task.Message = "No network interfaces"
			} else {
				task.Message = "Network interfaces:"
				task.ClearText = out.String()
			}

		case COMMAND_SERVICE:
			if !packer.CheckPacker([]string{"byte", "array"}) {
				goto DONE
			}
			ok := packer.ParseInt8()
			output := packer.ParseString()
			if ok == 0 {
				task.MessageType = adaptix.MESSAGE_ERROR
				task.Message = "Service operation failed:"
			} else {
				task.Message = "Service operation completed:"
			}
			task.ClearText = output

		case COMMAND_UNAME:
			if !packer.CheckPacker([]string{"array", "array", "array", "array", "array"}) {
				goto DONE
			}
			sysname := packer.ParseString()
			node := packer.ParseString()
			release := packer.ParseString()
			version := packer.ParseString()
			machine := packer.ParseString()
			task.Message = "System information:"
			task.ClearText = fmt.Sprintf("%s %s %s %s %s", sysname, node, release, version, machine)

		case COMMAND_CHMOD:
			if !packer.CheckPacker([]string{"array", "int"}) {
				goto DONE
			}
			path := packer.ParseString()
			mode := packer.ParseInt32()
			task.Message = fmt.Sprintf("Mode of '%s' set to %04o", path, mode)

		case COMMAND_USERS:
			if !packer.CheckPacker([]string{"int"}) {
				goto DONE
			}
			count := int(packer.ParseInt32())
			var out strings.Builder
			out.WriteString(fmt.Sprintf(" %-16s %-12s %-24s %s\n", "User", "TTY", "Host", "Login"))
			out.WriteString(fmt.Sprintf(" %-16s %-12s %-24s %s", "----", "---", "----", "-----"))
			for i := 0; i < count; i++ {
				if !packer.CheckPacker([]string{"array", "array", "array", "long"}) {
					goto DONE
				}
				user := packer.ParseString()
				tty := packer.ParseString()
				host := packer.ParseString()
				login := packer.ParseInt64()
				loginText := "-"
				if login > 0 {
					loginText = time.Unix(int64(login), 0).Format("02/01/2006 15:04")
				}
				out.WriteString(fmt.Sprintf("\n %-16s %-12s %-24s %s", user, tty, host, loginText))
			}
			if count == 0 {
				task.Message = "No interactive users"
			} else {
				task.Message = "Logged in users:"
				task.ClearText = out.String()
			}

		case COMMAND_SYSINFO:
			if !packer.CheckPacker([]string{"array", "array", "array", "array", "array", "int", "int", "int", "array", "long", "array", "long", "long", "long", "int", "array"}) {
				goto DONE
			}
			distro := packer.ParseString()
			kernel := packer.ParseString()
			machine := packer.ParseString()
			hostname := packer.ParseString()
			username := packer.ParseString()
			uid := packer.ParseInt32()
			euid := packer.ParseInt32()
			gid := packer.ParseInt32()
			groups := packer.ParseString()
			uptime := packer.ParseInt64()
			load := packer.ParseString()
			memTotal := packer.ParseInt64()
			memFree := packer.ParseInt64()
			memAvail := packer.ParseInt64()
			cpus := packer.ParseInt32()
			container := packer.ParseString()

			if container == "" {
				container = "none"
			}
			var out strings.Builder
			out.WriteString(fmt.Sprintf("Hostname    : %s\n", hostname))
			out.WriteString(fmt.Sprintf("Distribution: %s\n", distro))
			out.WriteString(fmt.Sprintf("Kernel      : %s (%s)\n", kernel, machine))
			out.WriteString(fmt.Sprintf("User        : %s (uid=%d euid=%d gid=%d)\n", username, uid, euid, gid))
			out.WriteString(fmt.Sprintf("Groups      : %s\n", groups))
			out.WriteString(fmt.Sprintf("Uptime      : %s\n", formatDuration(int64(uptime))))
			out.WriteString(fmt.Sprintf("Load        : %s\n", load))
			out.WriteString(fmt.Sprintf("Memory      : total %s, free %s, available %s\n",
				sizeBytesToFormat(int64(memTotal)), sizeBytesToFormat(int64(memFree)), sizeBytesToFormat(int64(memAvail))))
			out.WriteString(fmt.Sprintf("CPUs        : %d\n", cpus))
			out.WriteString(fmt.Sprintf("Container   : %s", container))
			task.Message = "Host information:"
			task.ClearText = out.String()
		}

		outTasks = append(outTasks, task)
	}

DONE:

	for _, task := range outTasks {
		Ts.TsTaskUpdate(agentData.Id, task)
	}
	return nil
}

func osFileModeString(mode uint32) string {
	return fileModeString(mode)
}

func formatDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	parts := make([]string, 0, 3)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	parts = append(parts, fmt.Sprintf("%dm", minutes))
	return strings.Join(parts, " ")
}
