// Command hostlier-terraform serves the optional approved saved-plan package protocol.
package main

import (
	"context"
	"fmt"
	"hostlier/packages/iac"
	"hostlier/packages/terraform"
	"hostlier/pkg/packagekit"
	"os"
)

// catalogPath is administrator-selected at build time, never supplied by a workflow.
var catalogPath = "/etc/hostlier/iac/terraform.json"

// main reports only sanitized adapter errors and reserves stdout for protocol JSON.
func main() {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run validates the installed immutable bundle catalog before serving a single request.
func run() error {
	catalog, err := iac.LoadCatalog(catalogPath)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	handler, err := terraform.New(catalog)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	// Discovery publishes only the static typed capability, without lifecycle input.
	if len(os.Args) == 2 && os.Args[1] == "describe" {
		describer, ok := handler.(packagekit.Describer)
		// Only packages with an explicit public contract can be discovered.
		if !ok {
			return fmt.Errorf("package capability unavailable")
		}
		return packagekit.Describe(describer, os.Stdout)
	}
	// Runner supplies a fixed operation argument; arbitrary command options are forbidden.
	if len(os.Args) != 2 || os.Args[1] != "serve" {
		return fmt.Errorf("expected describe or serve")
	}
	return packagekit.Serve(context.Background(), handler, os.Stdin, os.Stdout)
}
