//go:build linux

package playerui

import (
	"syscall"
	"unsafe"
)

func inTerminalSupported() bool { return true }

func isTerminal(fd int) bool {
	var state syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&state)))
	return errno == 0
}
