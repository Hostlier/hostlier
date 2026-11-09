package database_test

import (
	"testing"

	"hostlier/packages/aiven"
	"hostlier/packages/database"
	"hostlier/packages/railway"
)

// providerValidation represents the common public validation contract exercised through each provider package.
type providerValidation interface{ Validate() error }

// providerModeCase pairs an invalid public configuration with the lifecycle boundary it exercises.
type providerModeCase struct {
	name   string
	config providerValidation
}

// TestProviderModePresenceBoundaries keeps shared validation from accepting missing or contradictory paid-instance settings.
func TestProviderModePresenceBoundaries(t *testing.T) {
	cases := []providerModeCase{
		{name: "aiven missing instance", config: aiven.Config{Config: base("postgres", database.NewInstance)}},
		{name: "railway missing instance", config: railway.Config{Config: base("postgres", database.NewInstance)}},
		{name: "aiven mixed modes", config: aiven.Config{Config: base("postgres", database.NewDatabase), Instance: &aiven.Instance{}}},
		{name: "railway mixed modes", config: railway.Config{Config: base("postgres", database.NewDatabase), Instance: railwayInstance()}},
		{name: "aiven token collision", config: aiven.Config{Config: base("postgres", database.NewInstance), Instance: &aiven.Instance{TokenSecret: "app-url", Project: "project", Service: "service", Plan: "plan"}}},
	}
	// Each provider entry point must enforce shared mode and credential boundaries before interpreting placement.
	for _, test := range cases {
		// No invalid configuration may silently select provider defaults or ignore conflicting instance settings.
		if err := test.config.Validate(); err == nil {
			t.Fatalf("accepted %s", test.name)
		}
	}
}
