package controller

import "testing"

// controllerSnapshot reads authoritative committed SQLite data so secrecy checks cannot pass on missing files.
func controllerSnapshot(t *testing.T, c *Controller) []byte {
	t.Helper()
	data, err := c.store.Snapshot()
	// Every persistence assertion must inspect an actual committed snapshot.
	if err != nil {
		t.Fatal(err)
	}
	return data
}
