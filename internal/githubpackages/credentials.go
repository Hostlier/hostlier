package githubpackages

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Credentials uses explicit local tokens or the developer's existing GitHub CLI login.
func Credentials(ctx context.Context) string {
	// Standard GitHub CLI environment variables take precedence over saved credentials.
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		// Tokens remain in memory and are never serialized in package metadata.
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
	// Missing gh or an absent saved login still permits anonymous public repositories.
	if err != nil || len(data) > 8192 {
		return ""
	}
	return strings.TrimSpace(string(data))
}
