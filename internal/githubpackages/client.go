// Package githubpackages downloads versioned package artifacts on the CLI machine.
package githubpackages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const DefaultRepository = "Usefused/hostlier-packages"

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// Client carries CLI-local repository credentials and a bounded HTTP transport.
type Client struct {
	HTTP           *http.Client
	BaseURL, Token string
}

// New uses only the GitHub API and refuses insecure or credential-bearing redirects.
func New(token string) *Client {
	return &Client{BaseURL: "https://api.github.com", Token: token, HTTP: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: releaseRedirect}}
}

// releaseRedirect allows signed HTTPS asset downloads without forwarding GitHub credentials.
func releaseRedirect(request *http.Request, previous []*http.Request) error {
	// Never downgrade transport or follow unbounded redirect chains.
	if request.URL.Scheme != "https" || request.URL.User != nil || len(previous) >= 5 {
		return fmt.Errorf("unsafe GitHub download redirect")
	}
	request.Header.Del("Authorization")
	return nil
}

// ValidateSelection requires explicit source coordinates rather than an implicit moving latest release.
func ValidateSelection(repository, version string) error {
	// Restrict repository identities before composing API routes or Go module arguments.
	if !repositoryPattern.MatchString(repository) || strings.Contains(repository, "..") {
		return fmt.Errorf("GitHub repository must be OWNER/REPO")
	}
	// A tag or commit is mandatory, and path traversal has no valid version meaning.
	if !versionPattern.MatchString(version) || strings.Contains(version, "..") {
		return fmt.Errorf("an explicit --version TAG or COMMIT is required")
	}
	return nil
}

// read fetches one bounded response while keeping authentication and provider errors private.
func (c *Client) read(ctx context.Context, path, accept string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.BaseURL, "/")+path, nil)
	// Malformed internal request paths cannot send credentials.
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub request")
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// Private repositories use a local token; it never enters controller requests.
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.HTTP.Do(request)
	// Avoid echoing signed URLs or credential-bearing transport diagnostics.
	if err != nil {
		return nil, fmt.Errorf("cannot reach GitHub")
	}
	defer response.Body.Close()
	// GitHub error bodies can contain repository or token diagnostics; keep errors bounded and fixed.
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub returned HTTP %d; check repository, version, and local GitHub access", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	// A truncated or oversized download cannot be installed.
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("GitHub response incomplete or too large")
	}
	return data, nil
}

// Commit resolves an explicitly selected tag or branch to an immutable Git revision for SDK installation.
func (c *Client) Commit(ctx context.Context, repository, version string) (string, error) {
	// Validate before constructing an authenticated URL.
	if err := ValidateSelection(repository, version); err != nil {
		return "", err
	}
	raw, err := c.read(ctx, "/repos/"+repository+"/commits/"+url.PathEscape(version), "application/vnd.github+json", 4<<20)
	// Failed resolution must not fall back to a moving branch.
	if err != nil {
		return "", err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	// Only a full GitHub SHA can pin the following Go dependency operation.
	if json.Unmarshal(raw, &commit) != nil || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit.SHA) {
		return "", fmt.Errorf("invalid GitHub commit response")
	}
	return commit.SHA, nil
}
