// Command hostlier-pulumi serves the optional constrained saved-update-plan package.
package main

import (
	"context"
	"fmt"
	pulumi "hostlier-pulumi"
	"hostlier/packages/iac"
	"hostlier/pkg/packagekit"
	"os"
)

// catalogPath is controlled only by the administrator building and installing this package.
var catalogPath = "/etc/hostlier/iac/pulumi.json"

// main keeps provider diagnostics away from protocol stdout.
func main() {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run resolves no paths or commands from project-selected configuration.
func run() error {
	catalog, err := iac.LoadCatalog(catalogPath)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	handler, err := pulumi.New(catalog)
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
