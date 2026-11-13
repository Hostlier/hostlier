package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hostlier/internal/cli"
)

// main binds process signals to CLI cancellation and reports command failures.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Report the safe CLI failure and return a nonzero process status.
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Hostlier:", err)
		var status interface{ ExitCode() int }
		// Preserve remote command status for scripts using hostlier server ssh.
		if errors.As(err, &status) && status.ExitCode() > 0 {
			os.Exit(status.ExitCode())
		}
		os.Exit(1)
	}
}
