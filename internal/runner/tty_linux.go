package runner

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal reports whether f is connected to a controlling terminal.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// OpenPTY allocates a fresh pseudo-terminal and returns its master end and
// the path to the corresponding slave device. Giving a child process the
// slave as its stdout/stderr (instead of a plain os.Pipe) lets it correctly
// detect a real terminal - many test runners (cargo test, pytest, jest,
// vitest) check this to decide whether to emit ANSI color, which a plain
// pipe would silently turn off.
func OpenPTY() (master *os.File, slavePath string, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, "", fmt.Errorf("runner: opening /dev/ptmx: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, "", fmt.Errorf("runner: unlocking pty: %w", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, "", fmt.Errorf("runner: reading pty number: %w", err)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n), nil
}
