//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The agent is built with CGO_ENABLED=0, so the classic grantpt/unlockpt
// helpers are unavailable. The PTY is opened directly through /dev/ptmx and
// configured with two ioctls - this is the documented Linux interface and is
// what openpty(3) does internally.

const (
	ioctlTIOCSPTLCK = 0x40045431 // set/clear the slave lock
	ioctlTIOCGPTN   = 0x80045430 // get the slave pty number
	ioctlTIOCSWINSZ = 0x5414     // set window size
)

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func ioctlPtr(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// openPTY returns the master and slave ends of a new pseudo terminal.
func openPTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}

	unlock := int32(0)
	if err := ioctlPtr(master.Fd(), ioctlTIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		_ = master.Close()
		return nil, nil, err
	}

	var ptyNumber int32
	if err := ioctlPtr(master.Fd(), ioctlTIOCGPTN, unsafe.Pointer(&ptyNumber)); err != nil {
		_ = master.Close()
		return nil, nil, err
	}

	slavePath := fmt.Sprintf("/dev/pts/%d", ptyNumber)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}

	return master, slave, nil
}

func setWinsize(f *os.File, rows, cols int) error {
	ws := winsize{Row: uint16(rows), Col: uint16(cols)}
	return ioctlPtr(f.Fd(), ioctlTIOCSWINSZ, unsafe.Pointer(&ws))
}
