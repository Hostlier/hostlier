package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hostlier/internal/controllercmd"
)

// main binds process signals to controller cancellation and reports command failures.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Report the safe controller failure and return a nonzero process status.
	if err := controllercmd.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Hostlier controller:", err)
		os.Exit(1)
	}
}
