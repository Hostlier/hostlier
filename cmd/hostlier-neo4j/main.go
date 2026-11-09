// Command hostlier-neo4j hosts the optional neo4j resource and workflow package.
package main

import (
	"context"
	"fmt"
	"hostlier/internal/resourcepackage"
	"hostlier/packages/engines"
	"os"
)

// main selects only a fixed engine and a private protocol operation.
func main() {
	// Report fixed errors without copying private protocol diagnostics.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "resource package failed")
		os.Exit(1)
	}
}

// run exposes resource RPC and workflow serving over anonymous private pipes.
func run() error {
	// No package operation accepts credentials or runtime configuration in arguments.
	if len(os.Args) != 2 {
		return fmt.Errorf("expected resource or serve")
	}
	// Resource adapters are isolated from the lightweight controller executable.
	if os.Args[1] == "resource" {
		return resourcepackage.Serve("neo4j", os.Stdin, os.Stdout)
	}
	// Workflow requests use the same fixed engine and private transport.
	if os.Args[1] == "serve" {
		return engines.Serve(context.Background(), "neo4j", os.Stdin, os.Stdout)
	}
	return fmt.Errorf("unsupported package operation")
}
