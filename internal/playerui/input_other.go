//go:build !linux

package playerui

func inTerminalSupported() bool { return false }

func pollKeys(_ int, _ chan<- string, stop <-chan struct{}) {
	<-stop
}
