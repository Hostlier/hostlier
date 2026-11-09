// Command hostlier-package validates optional provider configurations and prints offline previews.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"hostlier/packages/aiven"
	"hostlier/packages/database"
	"hostlier/packages/railway"
	"hostlier/packages/usefused"
)

// planner allows the command to inspect packages without adding provider imports to the controller.
type planner interface {
	// Plan validates the provider configuration and returns a non-executable preview.
	Plan() (database.Plan, error)
}

// main reports validation failures without attempting cloud execution.
func main() {
	// All errors are local configuration or output failures.
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run offers offline previews and explicit server-local installation, with no remote apply operation.
func run(args []string, output io.Writer) error {
	// Local installation is explicit and never creates or deploys a remote database.
	if len(args) == 5 && args[0] == "install" {
		return usefused.Install(args[1], args[2], args[3], args[4])
	}
	// Avoid silently accepting options that imply provider execution.
	if len(args) != 3 || args[0] != "plan" {
		return errors.New("usage: hostlier-package plan <aiven|railway> <config.yaml> OR install <aiven|railway> <binary> <executor.json> <packages-dir>")
	}
	configuration, err := selectPackage(args[1])
	// Unknown providers cannot fall back to a default account or backend.
	if err != nil {
		return err
	}
	file, err := os.Open(args[2])
	// Avoid including file contents or secret values in error messages.
	if err != nil {
		return errors.New("cannot open package configuration")
	}
	defer file.Close()
	// Strict decoding prevents ignored settings and accidental inline credentials.
	if err := database.Decode(file, configuration); err != nil {
		return err
	}
	plan, err := configuration.Plan()
	// No output is emitted until the complete configuration validates.
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

// selectPackage instantiates only explicitly requested package configuration types.
func selectPackage(provider string) (planner, error) {
	// This tool bundles validators; the server controller imports none of them.
	switch provider {
	// Aiven supports its managed database and cache engines.
	case "aiven":
		return &aiven.Config{}, nil
	// Railway uses reviewed templates in an explicit project/environment.
	case "railway":
		return &railway.Config{}, nil
	// Provider names are deliberately closed until a package protocol is implemented.
	default:
		return nil, errors.New("unknown package: expected aiven or railway")
	}
}
