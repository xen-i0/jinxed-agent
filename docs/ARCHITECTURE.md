# Architecture

## 1. Repository layout

```
jinxed-agent/
├── README.md
├── LICENSE                     GPL-3.0 (inherited from AdaptixC2)
├── Makefile                    build/test entry points
├── .gitignore
├── client/
│   └── jinxed_linux.axs        operator command tree, menus, payload UI
├── docs/
│   ├── COMMAND_MATRIX.md       Phase 1 audit + opcode mapping
│   ├── PROTOCOL.md             byte-level wire contract
│   └── ARCHITECTURE.md         this file
└── src/
    ├── agent/                  standalone Go agent (stdlib only)
    │   ├── go.mod
    │   ├── main.go             flag parsing, beacon loop, sleep/jitter
    │   ├── agent.go            session state, beat builder, reply helpers
    │   ├── config.go           profile blob decoder
    │   ├── outbox.go           single response accumulator + wake channel
    │   ├── pack.go             LE reader (tasks) / BE writer (responses)
    │   ├── crypt.go            RC4
    │   ├── http.go             BeaconHTTP transport
    │   ├── commander.go        opcode dispatcher
    │   ├── protocol.go         opcode and state constants
    │   ├── sysinfo.go          host/kernel/user/IP discovery
    │   ├── jobs.go             process jobs, pgroup kill, output pump
    │   ├── terminal.go         PTY shell jobs
    │   ├── pty_linux.go        openpty via /dev/ptmx ioctls
    │   ├── downloader.go       chunked download + upload staging
    │   ├── tunnels.go          channel data plane, reverse listeners
    │   ├── cmd_files.go        cat/cd/ls/cp/mv/mkdir/rm/chmod/disks
    │   ├── cmd_proc.go         /proc process enumeration
    │   ├── cmd_net.go          /proc/net sockets, interfaces
    │   ├── cmd_host.go         env/uname/sysinfo/service/users
    │   ├── util.go             tokenizer, errno, passwd/group, sizes
    │   └── zz_profile_gen.go   generated at build time (placeholder in git)
    └── extender/
        ├── go.mod
        ├── Makefile            builds dist/ + stages the agent source
        ├── config.yaml         extender descriptor (watermark, listeners)
        ├── pl_main.go          plugin lifecycle, profiles, build, commands
        ├── pl_process.go       response stream interpretation
        ├── pl_packer.go        BE parser / LE serialiser
        ├── pl_utils.go         constants, crypto, parsing, staging
        └── e2e_test.go         full in-process protocol test
```

## 2. Component responsibilities

### 2.1 Agent (`src/agent`)

**Single beacon goroutine + producer goroutines.** Command execution is
serialised in the beacon goroutine; only three kinds of work run concurrently:

| Goroutine | Lifetime | Writes to the outbox |
| --- | --- | --- |
| beacon loop | process | command responses |
| job output pump | per process/shell job | `0x8437` output records |
| tunnel reader/writer | per channel | `64`/`66`/`69`/`70` records, `68` accepts |
| reverse listener | per `rportfwd` | `68` accept records |

All of them append to a single `outbox` protected by a mutex. `outbox.take()`
patches the big-endian total length into the first four bytes and resets to a
4-byte placeholder, so the wire format is produced in one place and ordering
across producers is preserved.

The outbox also carries a `wake` channel: when a background producer appends
output while the beacon is sleeping, the sleep is interrupted and the agent
beacons immediately. This keeps interactive terminals and shells responsive
without lowering the configured sleep.

**Process model.** Children are started with `Setpgid: true`; `jobs kill` sends
`SIGKILL` to the negative PID so pipelines die atomically. Shell jobs use a PTY
and `setsid`/`Setctty`. No shell is ever invoked implicitly for `ps run` — the
command line is tokenised with a small POSIX lexer (quotes, escapes) and
`execve`d directly. `shell` explicitly wraps the operator's string in
`/bin/sh -c '<quoted>'`.

**File descriptors.** Downloads are closed on stop/cancel/finish; uploads are
assembled in memory (1 MiB chunks from the extender) then written once; PTYs are
closed when the shell exits; tunnel sockets are closed after a 1-second drain
when the server closes the channel.

### 2.2 Extender (`src/extender`)

A Go `-buildmode=plugin` module implementing `adaptix.PluginAgent` and
`adaptix.ExtenderAgent`.

| Hook | Responsibility |
| --- | --- |
| `InitPlugin` | Stores the teamserver handle, module dir and watermark; asserts the teamserver interface |
| `GenerateProfiles` | Encodes listener + operator settings into the RC4 profile blob (§2 of PROTOCOL.md) |
| `BuildPayload` | Copies the agent source to a temp tree, writes `zz_profile_gen.go`, cross-compiles with `CGO_ENABLED=0`, validates the ELF magic, returns the bytes |
| `CreateAgent` | Parses the beat, maps Linux metadata to `AgentData` (`Os = OS_LINUX`, UTF-8 codepage), returns the extender instance |
| `Encrypt` / `Decrypt` | RC4 with the session key |
| `PackTasks` | LE length prefix + `data‖taskId` concatenation |
| `CreateCommand` | Translates the client's `args` map into the opcode payload |
| `ProcessData` | Parses BE response records, updates tasks, drives downloads/tunnels/terminals and the Unix GUI models |
| `TunnelCallbacks` / `TerminalCallbacks` | Build proxy tasks for tunnel/terminal traffic |

`BuildPayload` deliberately resolves the agent source at runtime
(`JINXED_AGENT_SRC` → `ModuleDir/src_agent` → `../agent` → `../src/agent`) so
the repository layout and the installed layout both work.

### 2.3 Client extension (`client/jinxed_linux.axs`)

Runs inside the teamserver's goja AXScript engine. It registers:

* session menus (exit, file browser, process browser, remote terminal, tunnels),
* file-browser actions (execute, download, chmod, remove) and upload events,
* download controls (pause/resume/cancel) and job stop,
* the complete command tree with argument schemas, help text and examples,
* the payload builder panel (`GenerateUI`).

All argument names match `CreateCommand`'s expectations exactly — the AXScript
layer is the schema, the extender is the implementation.

## 3. Adding a command

1. **Opcode** — pick a free value in `src/agent/protocol.go` and
   `src/extender/pl_utils.go` (Linux extensions start at 100).
2. **Client schema** — add an `ax.create_command(...)` with `addArg*` calls in
   `client/jinxed_linux.axs` and include it in `commands_linux`.
3. **Request** — add a `case` to `CreateCommand` in `pl_main.go` that builds the
   `PackArray` for the new opcode.
4. **Agent handler** — add a `case` in `processTasks` (`commander.go`) and a
   handler that reads arguments in order **and the trailing task id last**.
5. **Response** — emit a record with `a.reply(taskID, opcode, fn)` and add the
   matching `case` to `ProcessData` (`pl_process.go`).

Because the record format is not self-delimiting, the agent and extender must
agree on argument order; there is no runtime schema negotiation.

## 4. Compatibility constraints

* **Go toolchain.** Plugins are only loadable by a teamserver built with the
  same Go version, `GOEXPERIMENT`, `GOAMD64` and build flags. The shipped
  AdaptixC2 v1.2 binary is `go1.25.4 X:jsonv2,greenteagc`; the extender
  `Makefile` defaults to those values.
* **No `-trimpath` for the plugin.** Package build IDs must match the server's.
  `-trimpath` changes them and the loader rejects the plugin with
  `plugin was built with a different version of package …`.
* **Agent side is unconstrained.** `BuildPayload` uses `-trimpath`, `CGO_ENABLED=0`
  and `-ldflags="-s -w"`, producing a static ELF that runs on any Linux with a
  matching architecture.
* **`axc2` version.** `v1.2.0`, matching the teamserver module graph.

## 5. Testing

`src/extender/e2e_test.go` starts a faithful in-process BeaconHTTP listener,
builds a real agent with `BuildPayload`, executes it, and drives every command
through `CreateCommand → PackTasks → HTTP → agent → ProcessData`. Assertions
cover:

* registration metadata (Linux OS, hostname, user, session key),
* `getuid`, `uname`, `sysinfo`, `disks`, `ifconfig`, `netstat`, `users`,
* `pwd`, `cd`, `ls` (including the Unix GUI model), `cat`, `cp`, `mv`, `mkdir`,
  `chmod`, `rm`,
* `env list/get/set/unset`,
* `ps list` (including the Unix GUI model), `ps run` output capture, `jobs`,
* `shell` through `/bin/sh`,
* a 320 KB download verified byte-for-byte against the source,
* a full upload verified on disk,
* `profile killdate/workingtime/download.chunksize`,
* a remote terminal session (`echo` round-trip),
* `terminate process` and process exit.

Run it with:

```bash
cd src/extender && go test -run TestEndToEnd -v .
```

`TestBuildPayloadFromListener` builds a payload against a listener profile
dumped from a live server, which is useful for deployment validation without
the GUI.

## 6. Operational notes

* Listener page edits invalidate the baked-in prefix/suffix lengths; rebuild
  payloads after changing `page-payload`.
* `rportfwd` listeners are not torn down by `rportfwd stop` on the agent side
  (the teamserver removes the tunnel); restarting a listener on the same port
  therefore fails until the agent exits. This mirrors upstream beacon behaviour.
* `env set` changes the agent process environment only; it does not persist to
  the login session.
* Session codepage is UTF-8 end to end, so binary-safe commands (`cat` of a
  binary file, terminal output) are transmitted without lossy conversion.
* The agent writes nothing to stdout. `-debug` enables transport diagnostics on
  stderr; production payloads are silent.
