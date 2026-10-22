# Jinxed Linux Agent — Command Matrix & Architecture Mapping

This document is the Phase 1 audit output for the Jinxed Linux agent. It records
every operator-visible command discovered in the AdaptixC2 tree, the filtering
decision applied to each, and the exact Command ID (opcode) used on the wire.

## 1. Audit methodology

Sources that were read in full and cross-referenced:

| Source | What it contributes |
| --- | --- |
| `AdaptixServer/extenders/beacon_agent/ax_config.axs` | Operator command tree registered with the client (names, arguments, subcommands, aliases, menus, browser hooks) |
| `AdaptixServer/extenders/beacon_agent/src_beacon/beacon/Commander.h/.cpp` | Command ID constants and the exact request/response serialisation of every command |
| `.../beacon/Agent.cpp`, `AgentInfo.cpp`, `AgentConfig.cpp`, `Packer.cpp`, `Crypt.cpp`, `MainAgent.cpp` | Registration beat, crypto, framing, beacon loop |
| `.../beacon/Downloader.cpp`, `JobsController.cpp`, `Proxyfire.cpp`, `Pivotter.cpp` | Asynchronous state machines surfaced as commands |
| `AdaptixServer/extenders/beacon_agent/pl_main.go`, `pl_packer.go`, `pl_utils.go` | Extender side: `CreateAgent`, `PackTasks`, `ProcessData`, `CreateCommand`, profile generation |
| `AdaptixServer/extenders/beacon_listener_http/pl_transport.go`, `pl_main.go` | HTTP transport handshake and response templating |
| `AdaptixServer/extenders/gopher_agent/ax_config.axs`, `pl_main.go` | Upstream reference for Unix command naming and the `ListingFileDataUnix` / `ListingProcessDataUnix` GUI structures |
| `AdaptixServer/core/server/*.go`, `core/extender/*.go`, `core/axscript/*.go` | Teamserver plumbing (`TsAgentCreate`, `PackData`, `ProcessData`, command registration, OS selection) |

Everything below is therefore derived from shipped framework code, not from
documentation or guesswork.

## 2. Notation

| Notation | Meaning |
| --- | --- |
| `u8 / u16 / u32 / u64` | Unsigned integer, **big-endian** when the agent emits it, **little-endian** when the server emits it (see `docs/PROTOCOL.md` §4) |
| `str` | Request direction: `u32 LE length` + bytes (+ trailing NUL added by `PackArray`) |
| `bstr` | Response direction: `u32 BE length` + bytes |
| `bytes` | Request direction: `u32 LE length` + raw bytes |
| `bbytes` | Response direction: `u32 BE length` + raw bytes |
| `int64` | 8-byte **big-endian** (agent response only) |

Every agent response record has the shape `u32 taskId, u32 opcode, payload…`.
Every agent request record has the shape `u32 opcode, args…, u32 taskId` —
the task id is *trailing* because `PackTasks` concatenates `task.Data` with the
task id.

## 3. Platform-locked primitives — excluded from the Linux agent

These exist in the Windows beacon and/or upstream client but depend on Windows
kernel objects, PE loading, tokens, the registry, WMI or named pipes. They are
deliberately **not** implemented or registered for `jinxed_linux`.

| Upstream command / feature | Opcode(s) | Why it cannot be ported natively |
| --- | --- | --- |
| `execute bof` / BOF output | `50`, `51` | Beacon Object Files are x86/x64 COFF objects calling Windows APIs; there is no COFF loader or Win32 ABI on Linux |
| `link smb` | `38` | Requires Windows named pipes (`\\.\pipe\…`) and an SMB pivot transport |
| `link tcp` (pivot) | `38`, `39` | Child agent listens with a Windows socket stack and is driven through the parent's pivotter; pivot transport is Windows-only |
| `unlink` | `39` | Same pivot subsystem |
| `rev2self` | `23` | Windows token impersonation; POSIX has no analogue inside one process |
| `ps run -i` (impersonation) | `43` | `CreateProcessAsUser`; ignored by design on Linux |
| `ps run -s` (suspended) | `43` | `CREATE_SUSPENDED`; accepted on the wire but ignored |
| `terminate thread` (true thread kill) | `10` | A Go runtime cannot safely kill one thread; the Linux agent performs a graceful loop exit |
| BeaconDNS `burst` | `21` sub 5/6 | DNS tunnelling transport is not implemented for Linux (see §6) |
| Registry, WMI, service *creation*, injection, screenshot (GDI) | — | Win32-only subsystems; not present in the beacon command tree either |
| Shellcode / DLL / Service EXE payload formats | — | PE-specific artefact formats |

The client extension simply does not register these, so an operator cannot
select them for a Linux session.

## 4. Cross-platform functional core — implemented natively

All of the following are registered in `client/jinxed_linux.axs`, implemented in
`src/agent`, and parsed in `src/extender/pl_process.go`. Opcodes are identical
to the Windows beacon, so teamserver bookkeeping, task tracking, browsers and
the download manager work unchanged.

### 4.1 Filesystem

| Command | Opcode | Request (server → agent) | Response (agent → server) | POSIX implementation |
| --- | --- | --- | --- | --- |
| `cat` | `24` | `str path` | `bstr path, bbytes content` (max 2048 B) | `open(2)` + `read(2)` |
| `cd` | `8` | `str path` | `bstr cwd` | `chdir(2)` + `getcwd(3)` |
| `cp` | `12` | `str src, str dst` | *(empty)* | `open/read/write` with mode preservation; directory destination appends basename |
| `ls` | `14` | `str path` | `u8 ok`; ok: `bstr root, u32 count, {u8 isDir, int64 size, u32 mtime, u32 mode, bstr user, bstr group, bstr name}*`; error: `u32 errno` | `opendir/readdir/lstat`, `/etc/passwd`, `/etc/group` |
| `mkdir` | `27` | `str path` | `bbytes path` | `mkdir -p` semantics (`MkdirAll`, 0755) |
| `mv` | `18` | `str src, str dst` | *(empty)* | `rename(2)` with `EXDEV` copy+unlink fallback |
| `pwd` | `4` | *(none)* | `bstr cwd` | `getcwd(3)` |
| `rm` | `17` | `str path` | `u8 wasDirectory` | `unlink(2)` / recursive `remove(3)` for directories |
| `chmod` | `105` | `str path, u32 mode` | `bstr path, u32 mode` | `chmod(2)` |
| `disks` | `15` | *(none)* | `u8 ok`; ok: `u32 count, {bstr mount, bstr fstype, u64 total, u64 free}*` | `/proc/mounts` + `statfs(2)`; populates the GUI drive list with mount points |
| `download` | `32` | `str file` | see §4.5 | `open(2)` + chunked `read(2)` |
| `upload` | `33` | `u32 memoryId, str remote_path` | *(empty)* | staged chunks assembled in memory, written with `O_CREAT|O_TRUNC` |
| `exfil` | `35` | `u32 newState, u32 fileId` | `u32 fileId, u8 state` | download state machine |

### 4.2 Execution, processes and jobs

| Command | Opcode | Request | Response | POSIX implementation |
| --- | --- | --- | --- | --- |
| `ps list` | `41` | *(none)* | `u8 ok`; ok: `u32 count, {u32 pid, u32 ppid, bstr tty, bstr user, bstr arch, bstr name}*`; error: `u32 errno` | `/proc/<pid>/{stat,comm,exe}`, `/etc/passwd`, ELF class detection, tty decoding |
| `ps kill` | `42` | `u32 pid` | `u32 pid` | `kill(2)` with `SIGKILL` |
| `ps run` | `43` | `u8 output, u8 impersonate, u32 state, str cmdline` | `u32 pid, u8 output, bstr cmdline` | `fork/exec` in its own process group; optional stdout/stderr pipe registered as a job |
| `jobs list` | `46` | *(none)* | `u32 count, {u32 jobId, u16 type, u16 pid}*` | in-memory job table |
| `jobs kill` | `47` | `u32 jobId` | `u8 found, u32 jobId` | `kill(-pgid, SIGKILL)` |
| `getuid` | `22` | *(none)* | `u8 elevated, bstr domain, bstr user` | `geteuid(2)` + `getpwuid(3)` equivalent |
| `shell` (alias) | `43` | `u8 1, u8 0, u32 0, str "/bin/sh -c '<cmd>'"` | as `ps run` | `sh -c` through the process job |

### 4.3 Session and profile

| Command | Opcode | Request | Response | Notes |
| --- | --- | --- | --- | --- |
| `sleep` | `21` sub `1` | `u32 sub, u32 seconds, u32 jitter` | `u32 sub, u32 seconds, u32 jitter` | interval adapts immediately; server data updated by the extender |
| `profile download.chunksize` | `21` sub `2` | `u32 sub, u32 bytes` | `u32 sub, u32 bytes` | default 102400 bytes |
| `profile killdate` | `21` sub `3` | `u32 sub, u32 unixTime` | `u32 sub, u32 unixTime` | `0` disables |
| `profile workingtime` | `21` sub `4` | `u32 sub, u32 packedWindow` | `u32 sub, u32 packedWindow` | packed `startH<<24 | startM<<16 | endH<<8 | endM` |
| `terminate thread|process` | `10` | `u32 method` (`1`/`2`) | `u32 method` (exit record) | graceful loop exit vs process exit; both end the process |

### 4.4 Remote terminal

| Command | Opcode | Request | Response | POSIX implementation |
| --- | --- | --- | --- | --- |
| terminal start | `71` | `u32 terminalId, str program, u32 rows, u32 cols` | `u32 shellId, u32 0x8437, u8 4, u8 0` | `openpty` via `/dev/ptmx` + `TIOCSPTLCK`/`TIOCGPTN`, child in a new session with the slave as controlling tty |
| terminal input | `72` | `u32 terminalId, bytes data` | *(none)* | `write(2)` to the PTY master |
| terminal close | `47` | `u32 terminalId` | `u32 jobId, …` | `kill(-pgid, SIGKILL)` + PTY teardown |

Output and exit are delivered as `COMMAND_JOB` (`0x8437`) records:
`u32 shellId, u32 0x8437, u8 4, u8 state, [bstr output]` with state
`0`=starting, `1`=running (output), `3`=killed/closed, `2`=finished+`u32 errno`.

### 4.5 Download lifecycle

| Record | Opcode | Payload |
| --- | --- | --- |
| start | `32` | `u32 fileId, u8 1, int64 size, bstr fullPath` |
| chunk | `32` | `u32 fileId, u8 2, bbytes chunk` (default 100 KiB per beacon) |
| finish | `32` | `u32 fileId, u8 3` |
| state change | `35` | `u32 fileId, u8 state` (`1` running, `2` stopped, `3` finished, `4` cancelled) |

Pause/resume/cancel are `COMMAND_EXFIL` requests from the extender; the file
descriptor is closed on stop/cancel and the download is removed from the table.

### 4.6 Tunnels (SOCKS / port forwarding)

The teamserver owns the listening sockets; the agent is the data plane. No new
opcodes are introduced.

| Direction | Record | Purpose |
| --- | --- | --- |
| server → agent | `62 u32 channelId, u32 tunnelType, str address, u32 port` | open a TCP channel from the agent |
| agent → server | `62` + `channelId, u32 62, u32 type, u32 result` | connect result (`0` success, errno on failure) |
| server → agent | `64 u32 channelId, bytes data` | outbound bytes to the remote socket |
| agent → server | `64` + `channelId, u32 64, bbytes data` | inbound bytes from the remote socket |
| server → agent | `69`/`70 u32 channelId` | pause/resume the agent's reader (backpressure) |
| agent → server | `69`/`70` + `channelId` | agent's write queue crossed the high/low watermark |
| server → agent | `66 u32 channelId` | close |
| agent → server | `66` + `channelId, u32 66, u32 0, u32 0` | socket closed/errored |
| server → agent | `67 u32 tunnelId, u32 port` | start a reverse listener on the agent |
| agent → server | `67` + `tunnelId, u32 67, u32 5, u32 status` | listener status (`2` success, `0` failure) |
| agent → server | `68` + `tunnelId, u32 68, u32 channelId` | new accepted connection |

Supported client commands: `socks start|stop` (SOCKS4a/SOCKS5, optional auth),
`lportfwd start|stop`, `rportfwd start|stop`. UDP ASSOCIATE is answered with
`EAFNOSUPPORT`; the teamserver only uses TCP channels for CONNECT.

### 4.7 Upload staging

| Step | Opcode | Payload |
| --- | --- | --- |
| stage | `0x2321` | `u32 memoryId, u32 totalSize, bytes chunk` (silent) |
| commit | `33` | `u32 memoryId, str remotePath` |

The extender splits the file into 1 MiB `COMMAND_SAVEMEMORY` tasks before the
commit task, exactly like the Windows agent.

## 5. Linux-native extensions

These opcodes (`100–107`) are unique to Jinxed and do not collide with any
upstream value. They are implemented end-to-end (client → extender → agent →
extender → client).

| Command | Opcode | Request | Response | Implementation |
| --- | --- | --- | --- | --- |
| `env list` | `100` op `0` | `u32 op, str "", str ""` | `u32 op, u32 count, {bstr "K=V"}*` | `environ(7)` |
| `env get` | `100` op `1` | `u32 op, str name, str ""` | `u32 op, u8 found, bstr value` | `getenv(3)` |
| `env set` | `100` op `2` | `u32 op, str name, str value` | `u32 op, u8 ok, bstr detail` | `setenv(3)` (agent process scope) |
| `env unset` | `100` op `3` | `u32 op, str name, str ""` | `u32 op, u8 ok, bstr detail` | `unsetenv(3)` |
| `netstat` | `101` | *(none)* | `u32 count, {bstr proto, bstr local, bstr remote, bstr state, u32 uid, u64 inode}*` | `/proc/net/{tcp,tcp6,udp,udp6}` with little-endian address decoding |
| `ifconfig` | `102` | *(none)* | `u32 count, {bstr name, bstr addrs, u32 mtu, bstr flags, bstr hwaddr}*` | `getifaddrs(3)` equivalent (`net.Interfaces`) |
| `service` | `103` | `str action, str unit` | `u8 ok, bstr output` | `systemctl` with SysV `service` fallback, 15 s timeout |
| `uname` | `104` | *(none)* | `bstr sysname, bstr node, bstr release, bstr version, bstr machine` | `/proc/sys/kernel/*`, `uname(2)` equivalents |
| `chmod` | `105` | `str path, u32 mode` | `bstr path, u32 mode` | `chmod(2)` |
| `users` | `106` | *(none)* | `u32 count, {bstr user, bstr tty, bstr host, u64 login}*` | `utmp(5)` records (`USER_PROCESS`) |
| `sysinfo` | `107` | *(none)* | distro, kernel, machine, hostname, user, uid, euid, gid, groups, uptime, load, memTotal, memFree, memAvail, CPUs, container | `/etc/os-release`, `/proc/uptime`, `/proc/loadavg`, `/proc/meminfo`, cgroup detection |

## 6. Transport parity

| Listener | Windows beacon | Jinxed Linux | Rationale |
| --- | --- | --- | --- |
| `BeaconHTTP` | yes | **yes** | Full parity: POST callbacks, header heartbeat, page-prefix/suffix framing, URI/UA/Host-header rotation, HTTP(S) proxy |
| `BeaconTCP` | yes | no | bind-TCP agents are reachable only through a parent agent's pivot subsystem, which is Windows-only |
| `BeaconSMB` | yes | no | named pipes do not exist on Linux |
| `BeaconDNS` | yes | no | DNS tunnel codec is Windows-specific; not required for a working Linux session |

Beacon behaviour implemented on Linux: sleep + jitter, working-time windows,
kill date, interactive `sleep 0`, session key rotation per agent, RC4 profile
and traffic crypto, output batching, immediate re-beacon when output is pending.

## 7. Summary

| Category | Count |
| --- | --- |
| Operator commands discovered (beacon tree, including aliases and subcommands) | 34 |
| Platform-locked / excluded | 8 (plus 3 payload formats) |
| Cross-platform core implemented | 22 commands + 1 alias (`interact`) |
| Linux-native additions | 8 commands / 15 sub-actions |
| Opcodes shared byte-for-byte with the beacon | 30 |
| New opcodes introduced | 8 (`100–107`) |
