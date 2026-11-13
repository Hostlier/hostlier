package installer

import (
	"context"
	"strings"
)

// existingCertbotAvailable inspects Snap without launching its privileged sandbox
// inside the controller's NoNewPrivileges service. Certificate jobs already run
// through the separate Hostlier systemd template with Snap-compatible protections.
func existingCertbotAvailable(ctx context.Context, run func(context.Context, string, ...string) (string, bool), readlink func(string) (string, error)) bool {
	if _, ok := run(ctx, "/usr/bin/certbot", "--version"); ok {
		return true
	}
	// Accept only the standard launcher used by our existing certificate units.
	link, err := readlink("/usr/bin/certbot")
	if err != nil || link != "/snap/bin/certbot" {
		return false
	}
	launcher, err := readlink("/snap/bin/certbot")
	if err != nil || launcher != "/usr/bin/snap" {
		return false
	}
	// Listing installed packages contacts snapd without starting snap-confine.
	listing, ok := run(ctx, "/usr/bin/snap", "list", "certbot")
	return ok && activeCertbotSnap(listing)
}

// activeCertbotSnap rejects absent, disabled, broken, or ambiguous installations.
func activeCertbotSnap(listing string) bool {
	lines := strings.Split(strings.TrimSpace(listing), "\n")
	if len(lines) != 2 {
		return false
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 6 || fields[0] != "certbot" || fields[1] == "-" || fields[2] == "-" {
		return false
	}
	for _, note := range strings.Split(fields[5], ",") {
		if note == "disabled" || note == "broken" {
			return false
		}
	}
	return true
}
