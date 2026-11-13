package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestExistingCertbotInspection covers native installs and Snap probes under NNP.
func TestExistingCertbotInspection(t *testing.T) {
	header := "Name Version Rev Tracking Publisher Notes\n"
	for _, tc := range []struct {
		name, link, launcher, listing string
		native, snap, want            bool
	}{
		{name: "native", native: true, want: true},
		{name: "confined snap", link: "/snap/bin/certbot", launcher: "/usr/bin/snap", listing: header + "certbot 5.1.0 999 latest/stable certbot-eff classic", snap: true, want: true},
		{name: "missing"},
		{name: "other binary", link: "/usr/local/bin/certbot"},
		{name: "broken launcher", link: "/snap/bin/certbot", launcher: "/missing"},
		{name: "snapd unavailable", link: "/snap/bin/certbot", launcher: "/usr/bin/snap"},
		{name: "disabled", link: "/snap/bin/certbot", launcher: "/usr/bin/snap", listing: header + "certbot 5.1.0 999 latest/stable certbot-eff classic,disabled", snap: true},
		{name: "broken", link: "/snap/bin/certbot", launcher: "/usr/bin/snap", listing: header + "certbot 5.1.0 999 latest/stable certbot-eff broken", snap: true},
		{name: "wrong package", link: "/snap/bin/certbot", launcher: "/usr/bin/snap", listing: header + "other 5.1.0 999 latest/stable publisher classic", snap: true},
		{name: "ambiguous", link: "/snap/bin/certbot", launcher: "/usr/bin/snap", listing: header + "certbot 5.1.0 999 latest/stable certbot-eff classic\ncertbot 5.0.0 998 latest/stable certbot-eff disabled", snap: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(_ context.Context, path string, args ...string) (string, bool) {
				switch path + " " + strings.Join(args, " ") {
				case "/usr/bin/certbot --version":
					return "certbot 5.1.0", tc.native
				case "/usr/bin/snap list certbot":
					return tc.listing, tc.snap
				default:
					t.Fatalf("unexpected probe: %s %v", path, args)
					return "", false
				}
			}
			readlink := func(path string) (string, error) {
				switch path {
				case "/usr/bin/certbot":
					return tc.link, nil
				case "/snap/bin/certbot":
					return tc.launcher, nil
				default:
					return "", errors.New("missing")
				}
			}
			if got := existingCertbotAvailable(context.Background(), run, readlink); got != tc.want {
				t.Fatalf("available=%v, want %v", got, tc.want)
			}
		})
	}
}
