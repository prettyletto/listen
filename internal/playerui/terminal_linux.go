//go:build linux

package playerui

import (
	"errors"
	"syscall"
	"unsafe"
)

func isTerminal(fd int) bool {
	var state syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&state)))
	return errno == 0
}

func enterTerminal(fd int) (func() error, error) {
	var previous syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, unsafe.Pointer(&previous)); err != nil {
		return nil, err
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFL), 0)
	if errno != 0 {
		return nil, errno
	}
	raw := previous
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, syscall.TCSETS, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFL), flags|uintptr(syscall.O_NONBLOCK)); errno != 0 {
		_ = ioctl(fd, syscall.TCSETS, unsafe.Pointer(&previous))
		return nil, errno
	}
	return func() error {
		termErr := ioctl(fd, syscall.TCSETS, unsafe.Pointer(&previous))
		_, _, flagsErr := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFL), flags)
		if flagsErr != 0 {
			return errors.Join(termErr, flagsErr)
		}
		return termErr
	}, nil
}

func terminalSize(fd int) (int, int, error) {
	var size struct {
		Row, Col, Xpixel, Ypixel uint16
	}
	if err := ioctl(fd, syscall.TIOCGWINSZ, unsafe.Pointer(&size)); err != nil {
		return 0, 0, err
	}
	return int(size.Col), int(size.Row), nil
}

func ioctl(fd int, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}
