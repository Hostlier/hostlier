package controllercmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"hostlier/internal/migration"
)

// TestMigrationCLIReviewAndApproval keeps this migration step within its validated recovery boundary.
func TestMigrationCLIReviewAndApproval(t *testing.T) {
	// Exercise each fixture case against the same recovery invariant.
	for _, tc := range []struct {
		name                                 string
		args                                 []string
		block, approve, wantApply, wantError bool
	}{
		{"preview", []string{"plan"}, false, false, false, false},
		{"declined", []string{"apply"}, false, false, false, true},
		{"approved", []string{"apply"}, false, true, true, false},
		{"explicit", []string{"apply", "--yes"}, false, false, true, false},
		{"blocked", []string{"apply", "--yes"}, true, true, false, true},
		{"dry", []string{"apply", "--dry-run", "--yes"}, false, true, false, false},
	} {
		// Supply isolated fixture behavior without contacting a live server.
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			applied := false
			args := append(tc.args, "--backup-dir", "/tmp/migration-backup")
			// Supply isolated fixture behavior without contacting a live server.
			deps := migrationDependencies{inspect: func(context.Context, migration.Options) (migration.Plan, error) {
				p := migration.Plan{Fingerprint: "reviewed", Changes: []string{"maintenance window"}}
				// Check the observed result against the fixture's migration invariant.
				if tc.block {
					p.Errors = []string{"conflict"}
				}
				return p, nil
				// Supply isolated fixture behavior without contacting a live server.
			}, confirm: func() (bool, error) {
				// Check the observed result against the fixture's migration invariant.
				if !strings.Contains(out.String(), "reviewed") {
					t.Fatal("approval before preview")
				}
				return tc.approve, nil
				// Supply isolated fixture behavior without contacting a live server.
			}, apply: func(_ context.Context, p migration.Plan, approved bool, _ func(string)) error {
				applied = approved
				return nil
			}}
			err := runMigration(t.Context(), args, strings.NewReader(""), &out, &diagnostics, deps)
			// Check the observed result against the fixture's migration invariant.
			if (err != nil) != tc.wantError || applied != tc.wantApply {
				t.Fatal(err, applied, out.String())
			}
		})
	}
}

// TestMigrationRollbackDryRunAndStatusNeverMutate keeps this migration step within its validated recovery boundary.
func TestMigrationRollbackDryRunAndStatusNeverMutate(t *testing.T) {
	// Exercise each fixture case against the same recovery invariant.
	for _, args := range [][]string{{"status"}, {"rollback", "--dry-run", "--yes"}} {
		called := false
		// Supply isolated fixture behavior without contacting a live server.
		deps := migrationDependencies{read: func(string) (*migration.Journal, error) { return &migration.Journal{Phase: "completed"}, nil }, rollback: func(context.Context, string, bool, func(string)) error { called = true; return nil }}
		var out bytes.Buffer
		// Check the observed result against the fixture's migration invariant.
		if err := runMigration(t.Context(), args, strings.NewReader(""), &out, &out, deps); err != nil {
			t.Fatal(err)
		}
		// Check the observed result against the fixture's migration invariant.
		if called {
			t.Fatal("read-only migration command mutated")
		}
	}
}
