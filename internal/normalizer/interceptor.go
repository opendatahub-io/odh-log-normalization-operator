package normalizer

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// OpenInput returns a reader from a named pipe (if pipePath is set) or stdin.
// When pipePath is non-empty, it creates the FIFO if it doesn't exist, then
// opens it for reading (blocks until a writer opens the other end).
func OpenInput(pipePath string) (io.ReadCloser, error) {
	if pipePath == "" {
		return os.Stdin, nil
	}

	if err := syscall.Mkfifo(pipePath, 0666); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("creating pipe %s: %w", pipePath, err)
	}

	f, err := os.Open(pipePath)
	if err != nil {
		return nil, fmt.Errorf("opening pipe %s: %w", pipePath, err)
	}

	return f, nil
}
