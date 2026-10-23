package runner

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal reports whether f is connected to a controlling terminal.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}

// OpenPTY isn't implemented on macOS yet. Callers already treat an error
// as "use a plain pipe instead", so the only cost is that a test runner
// whose output fastci captures can't tell it's on a terminal and may
// drop its ANSI color.
func OpenPTY() (master *os.File, slavePath string, err error) {
	return nil, "", errors.New("runner: pty allocation is not implemented on this platform")
}
