//go:build !linux

package playerui

import "errors"

func inTerminalSupported() bool { return false }

func isTerminal(int) bool { return false }

func enterTerminal(int) (func() error, error) {
	return nil, errors.New("interactive playback controls are available on Linux terminals")
}

func terminalSize(int) (int, int, error) { return 80, 24, nil }

func pollKeys(_ int, _ chan<- string, stop <-chan struct{}) {
	<-stop
}
