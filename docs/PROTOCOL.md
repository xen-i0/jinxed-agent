# Protocol Reference

This is the byte-level contract between the Jinxed Linux agent, the Jinxed
extender and an AdaptixC2 v1.2 teamserver. It is a compatible re-implementation
of the `beacon` protocol: any value that the teamserver, the client or the GUI
inspects is identical, including the endianness asymmetry described in §4.

## 1. Roles

```
Adaptix client ──WebSocket──> Teamserver ──plugin API──> Extender (.so)
                                  │                         │
                                  │                    PackTasks / ProcessData
                                  │                         │
                             BeaconHTTP listener  ──HTTP──> Linux agent
```

* The **listener** owns the HTTP endpoint and the heartbeat header.
* The **extender** owns profiles, payload builds, crypto, serialisation and
  response interpretation.
* The **agent** owns execution.

## 2. Profile blob (compile-time configuration)

`GenerateProfiles` produces one blob per listener profile:

```
u32 LE cryptLen | RC4(listenerKey, params) [cryptLen] | listenerKey [16]
```

* `listenerKey` is the listener's `encrypt_key` (16 bytes, 32 hex chars in the
  listener configuration). It is embedded in clear so the agent can decrypt
  `params` and encrypt the registration beat.
* `params` is a `PackArray` (see §4.2) of the fields below.

| # | Field | Type |
| --- | --- | --- |
| 1 | agent watermark (agent type) | int |
| 2 | kill date (unix) | int |
| 3 | working time (packed) | int |
| 4 | sleep seconds | int |
| 5 | jitter percent | int |
| 6 | listener watermark | int |
| 7 | use SSL | bool |
| 8 | server count *N* | int |
| 9 | *N* × hostname | string |
| 10 | *N* × port | int |
| 11 | HTTP method | string |
| 12 | URI count *U* | int |
| 13 | *U* × URI | string |
| 14 | heartbeat header name | string |
| 15 | User-Agent count *A* | int |
| 16 | *A* × User-Agent | string |
| 17 | extra request headers (`Name: value\r\n…`) | string |
| 18 | page prefix length (`ans_pre_size`) | int |
| 19 | page suffix length | int |
| 20 | Host-header count *H* | int |
| 21 | *H* × Host header | string |
| 22 | rotation mode (`0` sequential, `1` random) | int |
| 23 | proxy type (`0` none, `1` http, `2` https) | int |
| 24 | proxy host | string |
| 25 | proxy port | int |
| 26 | proxy username | string |
| 27 | proxy password | string |

The page prefix/suffix are the parts of the listener's `page-payload` before
and after the `<<<PAYLOAD_DATA>>>` marker. They are frozen at build time — if
an operator edits the listener page afterwards, existing agents must be rebuilt.

## 3. Registration beat

The agent builds this structure once at start-up:

| Offset | Size | Field | Value |
| --- | --- | --- | --- |
| 0 | 4 | agent type | profile watermark (e.g. `0xc0de11fe`) |
| 4 | 4 | agent id | 4 random bytes |
| 8 | 4 | sleep | seconds |
| 12 | 4 | jitter | percent |
| 16 | 4 | kill date | unix seconds, 0 = off |
| 20 | 4 | working time | packed window |
| 24 | 2 | ACP | `65001` (UTF-8) |
| 26 | 2 | OEM CP | `65001` |
| 28 | 1 | GMT offset | signed hours as `int8` |
| 29 | 2 | PID | `uint16` (low 16 bits) |
| 31 | 2 | TID | `uint16` |
| 33 | 4 | kernel patch | `uint32` |
| 37 | 1 | kernel major | `uint8` |
| 38 | 1 | kernel minor | `uint8` |
| 39 | 4 | internal IPv4 | `octet0 | octet1<<8 | octet2<<16 | octet3<<24` |
| 43 | 1 | flags | bit0 arch64, bit1 sys64, bit2 elevated, bit3 is_server |
| 44 | 4+16 | session key | big-endian length + 16 random bytes |
| … | 4+n | domain | `""` on Linux |
| … | 4+n | computer | hostname |
| … | 4+n | username | `getpwuid(getuid())` |
| … | 4+n | process | image basename |
| … | 4+n | **os release** | `PRETTY_NAME` from `/etc/os-release` *(Jinxed extension)* |
| … | 4+n | **kernel release** | `uname -r` *(Jinxed extension)* |
| … | 4+n | **machine** | `x86_64`, `aarch64`, … *(Jinxed extension)* |

All integers and lengths in the beat are **big-endian** (C++ `Packer`).
The leading two `u32` (agent type, agent id) are what the HTTP listener uses to
route the request; the listener strips them before calling `CreateAgent`.

The plaintext beat is RC4-encrypted with the listener key and base64-encoded
into the heartbeat header on **every** request (not only the first).

## 4. Endianness asymmetry (critical)

| Direction | Integers | Length prefixes | Producer |
| --- | --- | --- | --- |
| server → agent | little-endian | little-endian | Go `PackArray` |
| agent → server | big-endian | big-endian | C++ `Packer` equivalent |

This is inherited from upstream Adaptix: the Go extender writes LE in
`PackArray`, while the agent's `Packer` writes BE. An implementation that uses
one endianness in both directions will register successfully and then fail on
the first command.

## 5. HTTP exchange

### Request

```
<METHOD> <URI> HTTP/1.1
Host: <callback host or configured Host header>
User-Agent: <rotated UA>
<extra listener headers>
<heartbeat header>: base64(RC4(listenerKey, beat))

RC4(sessionKey, responseStream)      ← body, omitted when idle
```

* One connection per beacon (`Connection: close` semantics), TLS verification
  disabled (self-signed lab certs), HTTP/HTTPS proxy optional.
* Server/URI/UA/Host-header rotation happens once per beacon; on connection
  failure the next server is tried immediately.
* When there is nothing to send, the body is empty — the agent still checks in.

### Response

```
<page prefix><RC4(sessionKey, taskStream)><page suffix>
```

The agent strips exactly `ans_pre_size` bytes from the front and the page
suffix from the back, then RC4-decrypts. A non-200 status or a body shorter
than the page template is treated as "no tasks".

## 6. Task stream (server → agent)

```
u32 LE payloadLen
repeat payloadLen bytes:
    u32 LE opcode
    … opcode-specific arguments …
    u32 LE taskId          ← trailing, appended by PackTasks
```

Records are **not** self-delimiting: the agent must know each opcode's argument
layout. An unknown opcode aborts parsing of the remaining buffer (the agent
logs it in `-debug` mode) instead of desynchronising subsequent records.

## 7. Response stream (agent → server)

```
u32 BE totalLen            ← includes these 4 bytes
repeat:
    u32 BE taskId          ← channelId for tunnel records, jobId for job records
    u32 BE opcode
    … payload …
```

Error record: `u32 taskId, u32 0x1111ffff, u32 errno`. The extender renders
`errno` with the Linux errno table (`posixErrString`) instead of
`TsWin32Error`.

## 8. Asynchronous records

### 8.1 Jobs (`0x8437`)

```
u32 jobId, u32 0x8437, u8 jobType, u8 state [, bstr output | u32 errno]
```

* `jobType`: 3 = process, 4 = shell/terminal.
* `state`: 0 starting, 1 running (payload = output), 2 finished (payload = errno
  for shells), 3 killed/closed.
* A process job's final output and its `finished` record may arrive in the same
  beacon. A shell's `killed` record tells the extender to close the GUI terminal.

### 8.2 Download

See `docs/COMMAND_MATRIX.md` §4.5. Chunks are streamed one per beacon, so a
320 KB file takes four beacons at the default 100 KiB chunk size. `exfil stop`
leaves the descriptor open but skips the record; `exfil cancel` closes and
removes it.

### 8.3 Tunnels

Tunnel records reuse the task-id field as a channel id. Because they are not
real tasks, the extender intercepts them before task bookkeeping and forwards
them to `TsTunnelConnection*`.

Flow control: the agent buffers up to 4 MB of writes per channel. Above the
high watermark it emits `PAUSE` (opcode 69) so the server stops feeding it;
below 1 MB it emits `RESUME` (70). Above 16 MB it tears the channel down. In the
other direction the server can pause the agent's reader with the same opcodes.

## 9. Terminal (PTY) specifics

For a shell job the agent allocates a PTY pair (`/dev/ptmx`, `TIOCSPTLCK`,
`TIOCGPTN`, `/dev/pts/N`), applies `TIOCSWINSZ` from the client's terminal size,
and starts the program with `setsid` + `Setctty` so job control and signals
work. Output is read from the master and shipped as UTF-8 strings; because the
session codepage is `65001`, no conversion happens in the extender.

## 10. Session codepage

The agent advertises `ACP = OEMCP = 65001`. The teamserver's encoding helpers
map unknown codepages to identity, so all strings cross the boundary as UTF-8
without conversion or corruption. This is why `TsConvertCpToUTF8(...)` calls in
the extender are safe no-ops for Linux sessions.

## 11. Beacon loop

```
forever:
    if terminated: emit exit record, flush, exit
    flush pending response stream (if any)
    HTTP exchange
    parse task stream, execute commands (responses queued)
    pump downloader (one chunk per active download)
    if terminated: emit exit record, flush, exit
    if output pending: continue immediately       # low-latency output
    sleep(working window ? until window : sleep ± jitter)
```

Background producers (job readers, tunnel sockets, PTY readers) append to the
same response stream under a mutex and wake the loop early, so output is sent
on the next beacon without waiting for the full sleep interval.
