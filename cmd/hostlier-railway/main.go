// Command hostlier-railway runs optional direct provider workflows with explicit Usefused opt-in.
package main

import (
	"fmt"
	"hostlier/packages/providers"
	"os"
)

// main exposes only the bounded package protocol; no credentials are accepted as command arguments.
func main() {
	// Report fixed safe errors while preserving nonzero exit status for the workflow controller.
	if err := providers.Main("railway"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
