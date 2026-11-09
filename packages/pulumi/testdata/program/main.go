// The offline fixture has only a pinned local provider and file-system effects under its test directory.
package main

import (
	"fmt"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"path/filepath"
)

// file is a minimal Go binding for the deliberately local integration-test provider.
type file struct {
	pulumi.CustomResourceState
	Value pulumi.StringOutput `pulumi:"value"`
}

// main runs a compiled program; neither Go nor a package download is needed at deployment time.
func main() {
	// Validate each entry without exposing private values to the public protocol.
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg, err := configuration(ctx)
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return err
		}
		root := cfg["directory"]
		value := cfg["value"]
		first := &file{}
		// Stop this phase when private storage or native execution cannot be verified.
		if err := ctx.RegisterResource("local:index:File", "first", pulumi.Map{"path": pulumi.String(filepath.Join(root, "first.txt")), "value": pulumi.String(value)}, first, pulumi.Version("0.0.1")); err != nil {
			return err
		}
		second := &file{}
		// Stop this phase when private storage or native execution cannot be verified.
		if err := ctx.RegisterResource("local:index:File", "second", pulumi.Map{"path": pulumi.String(filepath.Join(root, "second.txt")), "value": pulumi.String(cfg["second"])}, second, pulumi.Version("0.0.1"), pulumi.DependsOn([]pulumi.Resource{first})); err != nil {
			return err
		}
		ctx.Export("value", first.Value)
		ctx.Export("private", pulumi.ToSecret(pulumi.String("sentinel-private-output")))
		return nil
	})
}

// configuration requires explicit fixture inputs without an additional configuration-library dependency.
func configuration(ctx *pulumi.Context) (map[string]string, error) {
	values := map[string]string{}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, key := range []string{"directory", "value", "second"} {
		value, ok := ctx.GetConfig(ctx.Project() + ":" + key)
		// Reject an identity or visibility mismatch before publishing the operation result.
		if !ok {
			return nil, fmt.Errorf("missing fixture configuration")
		}
		values[key] = value
	}
	return values, nil
}
