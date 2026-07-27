package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "--install" {
		installSelf(os.Args[2])
		return
	}

	pipePath := os.Getenv("LOG_NORMALIZER_PIPE_PATH")
	if pipePath == "" {
		pipePath = "/var/run/lognormalizer/log.pipe"
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "wrapper: no command specified")
		os.Exit(1)
	}

	for !isFIFO(pipePath) {
		time.Sleep(100 * time.Millisecond)
	}

	pipeFile, err := os.OpenFile(pipePath, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wrapper: opening pipe: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = pipeFile.Close() }()

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.MultiWriter(os.Stdout, pipeFile)
	cmd.Stderr = io.MultiWriter(os.Stderr, pipeFile)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wrapper: starting command: %v\n", err)
		os.Exit(1)
	}

	go func() {
		sig := <-sigCh
		_ = cmd.Process.Signal(sig)
	}()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "wrapper: %v\n", err)
		os.Exit(1)
	}
}

// installSelf copies the wrapper binary (/proc/self/exe) to the given destination
// path. Used by the init container to place the wrapper into the shared volume
// so app containers (which use arbitrary images) can invoke it.
func installSelf(dest string) {
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wrapper: reading self: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = self.Close() }()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wrapper: creating %s: %v\n", dest, err)
		os.Exit(1)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, self); err != nil {
		fmt.Fprintf(os.Stderr, "wrapper: copying to %s: %v\n", dest, err)
		os.Exit(1)
	}
}

func isFIFO(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeNamedPipe != 0
}
