package nginx

import "testing"

// TestLocalACMEBoundary keeps the test injection out of public issuers and trust settings.
func TestLocalACMEBoundary(t *testing.T) {
	// Validate this fixture boundary before continuing.
	for _, value := range []string{"http://127.0.0.1:14000/dir", "https://example.com/dir", "https://localhost/dir", "https://user@127.0.0.1/dir", "https://127.0.0.1/dir?x=1"} {
		// Validate this fixture boundary before continuing.
		if _, err := localACMEArguments(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	args, err := localACMEArguments("https://127.0.0.1:14000/dir")
	// Validate this fixture boundary before continuing.
	if err != nil || len(args) != 2 || args[0] != "--server" {
		t.Fatal(args, err)
	}
	args, err = localACMEArguments("")
	// Validate this fixture boundary before continuing.
	if err != nil || len(args) != 0 {
		t.Fatal("production default changed")
	}
}
