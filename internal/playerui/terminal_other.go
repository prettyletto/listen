//go:build !linux

package playerui

func inTerminalSupported() bool { return false }

func isTerminal(int) bool { return false }
