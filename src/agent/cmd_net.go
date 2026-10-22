package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type socketEntry struct {
	proto  string
	local  string
	remote string
	state  string
	uid    uint32
	inode  uint64
}

var tcpStates = map[string]string{
	"01": "ESTABLISHED",
	"02": "SYN_SENT",
	"03": "SYN_RECV",
	"04": "FIN_WAIT1",
	"05": "FIN_WAIT2",
	"06": "TIME_WAIT",
	"07": "CLOSE",
	"08": "CLOSE_WAIT",
	"09": "LAST_ACK",
	"0A": "LISTEN",
	"0B": "CLOSING",
}

func decodeProcAddress(addr string) string {
	idx := strings.LastIndexByte(addr, ':')
	if idx < 0 {
		return addr
	}
	hexIP := addr[:idx]
	port, err := strconv.ParseUint(addr[idx+1:], 16, 32)
	if err != nil {
		return addr
	}

	if len(hexIP) == 8 {
		v, err := strconv.ParseUint(hexIP, 16, 32)
		if err != nil {
			return addr
		}
		ip := net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		return net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
	}

	if len(hexIP) == 32 {
		var b [16]byte
		for w := 0; w < 4; w++ {
			v, err := strconv.ParseUint(hexIP[w*8:(w+1)*8], 16, 32)
			if err != nil {
				return addr
			}
			b[w*4+0] = byte(v)
			b[w*4+1] = byte(v >> 8)
			b[w*4+2] = byte(v >> 16)
			b[w*4+3] = byte(v >> 24)
		}
		return net.JoinHostPort(net.IP(b[:]).String(), strconv.Itoa(int(port)))
	}
	return addr
}

func parseProcNet(path, proto string, tcp bool) []socketEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var out []socketEntry
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		e := socketEntry{
			proto:  proto,
			local:  decodeProcAddress(f[1]),
			remote: decodeProcAddress(f[2]),
		}
		if tcp {
			e.state = tcpStates[strings.ToUpper(f[3])]
			if e.state == "" {
				e.state = f[3]
			}
		} else {
			e.state = "UNCONN"
			if strings.ToUpper(f[3]) == "01" {
				e.state = "ESTABLISHED"
			}
		}
		if uid, err := strconv.ParseUint(f[7], 10, 32); err == nil {
			e.uid = uint32(uid)
		}
		if inode, err := strconv.ParseUint(f[9], 10, 64); err == nil {
			e.inode = inode
		}
		out = append(out, e)
	}
	return out
}

func listSockets() []socketEntry {
	var all []socketEntry
	all = append(all, parseProcNet("/proc/net/tcp", "tcp", true)...)
	all = append(all, parseProcNet("/proc/net/tcp6", "tcp6", true)...)
	all = append(all, parseProcNet("/proc/net/udp", "udp", false)...)
	all = append(all, parseProcNet("/proc/net/udp6", "udp6", false)...)
	return all
}

func (a *agent) cmdNetstat(r *reader) {
	taskID := r.u32()
	socks := listSockets()

	a.reply(taskID, cmdNetstat, func(w *writer) {
		w.u32(uint32(len(socks)))
		for _, s := range socks {
			w.str(s.proto)
			w.str(s.local)
			w.str(s.remote)
			w.str(s.state)
			w.u32(s.uid)
			w.u64(s.inode)
		}
	})
}

// ---------------------------------------------------------------------------
// ifconfig
// ---------------------------------------------------------------------------

func (a *agent) cmdIfconfig(r *reader) {
	taskID := r.u32()

	ifaces, err := net.Interfaces()
	if err != nil {
		a.replyErr(taskID, err)
		return
	}

	a.reply(taskID, cmdIfconfig, func(w *writer) {
		w.u32(uint32(len(ifaces)))
		for _, iface := range ifaces {
			var addrs []string
			if list, err := iface.Addrs(); err == nil {
				for _, ad := range list {
					addrs = append(addrs, ad.String())
				}
			}
			hw := iface.HardwareAddr.String()
			if hw == "" {
				hw = "-"
			}
			w.str(iface.Name)
			w.str(strings.Join(addrs, ", "))
			w.u32(uint32(iface.MTU))
			w.str(iface.Flags.String())
			w.str(hw)
		}
	})
}

func formatSocketLine(s socketEntry) string {
	return fmt.Sprintf("%-5s %-24s %-24s %-12s uid=%-5d inode=%d", s.proto, s.local, s.remote, s.state, s.uid, s.inode)
}
