//go:build linux

package playerui

import (
	"syscall"
	"time"
)

func inTerminalSupported() bool { return true }

func pollKeys(fd int, keys chan<- string, stop <-chan struct{}) {
	var escapeState uint8
	lastEscape := time.Time{}
	for {
		select {
		case <-stop:
			return
		default:
		}
		if escapeState != 0 && time.Since(lastEscape) > 120*time.Millisecond {
			escapeState = 0
		}
		var buffer [64]byte
		count, err := syscall.Read(fd, buffer[:])
		if err != nil {
			if err != syscall.EAGAIN && err != syscall.EWOULDBLOCK && err != syscall.EINTR {
				return
			}
			if !waitForKey(stop, 20*time.Millisecond) {
				return
			}
			continue
		}
		if count == 0 {
			if !waitForKey(stop, 20*time.Millisecond) {
				return
			}
			continue
		}
		for _, value := range buffer[:count] {
			key, complete := decodeKey(value, &escapeState, &lastEscape)
			if !complete {
				continue
			}
			select {
			case keys <- key:
			case <-stop:
				return
			}
		}
	}
}

func waitForKey(stop <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

func decodeKey(value byte, escapeState *uint8, lastEscape *time.Time) (string, bool) {
	if *escapeState == 1 {
		if value == '[' || value == 'O' {
			*escapeState = 2
			*lastEscape = time.Now()
			return "", false
		}
		*escapeState = 0
	}
	if *escapeState == 2 {
		*escapeState = 0
		switch value {
		case 'A':
			return "up", true
		case 'B':
			return "down", true
		case 'C':
			return "right", true
		case 'D':
			return "left", true
		default:
			return "", false
		}
	}
	if value == 0x1b {
		*escapeState = 1
		*lastEscape = time.Now()
		return "", false
	}
	switch value {
	case '\r', '\n':
		return "enter", true
	case ' ':
		return "space", true
	case 3:
		return "ctrl+c", true
	default:
		return string(value), true
	}
}
