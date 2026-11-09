package cli

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hostlier/internal/githubpackages"
	"hostlier/internal/gitpackages"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
)

const officialPackageCatalog = "https://github.com/" + githubpackages.DefaultRepository + ".git"

// packageRepository selects the official Git catalog unless the caller explicitly requests GitHub releases.
func (s *session) packageRepository() string {
	// An explicit release transport is independent of Git catalogs.
	if s.flags["github"] != "" {
		return ""
	}
	return defaultValue(s.flags["repository"], officialPackageCatalog)
}

// validatePackageRepository validates the selected transport without discovering credentials.
func (s *session) validatePackageRepository(requireVersion bool) error {
	// A request cannot claim both a provider release and a different Git source.
	if s.flags["repository"] != "" && s.flags["github"] != "" {
		return model.Err("ARGUMENTS", "Select either --repository GIT_URL or --github OWNER/REPO")
	}
	version := s.flags["version"]
	// Installation must be reproducible even when catalog browsing follows a moving default.
	if requireVersion && version == "" {
		return model.Err("ARGUMENTS", "Package installation requires --version TAG_OR_COMMIT")
	}
	// Generic Git catalogs support HEAD discovery without assuming release APIs exist.
	if repository := s.packageRepository(); repository != "" {
		return gitpackages.ValidateSelection(repository, version)
	}
	return githubpackages.ValidateSelection(s.flags["github"], defaultValue(version, "latest"))
}

// packageCatalog dispatches discovery by source transport while sharing one catalog format.
func (s *session) packageCatalog() (packageexec.Catalog, error) {
	// Git URLs work independently of GitHub release conventions.
	if repository := s.packageRepository(); repository != "" {
		return s.gitClient().Available(s.ctx, repository, s.flags["version"])
	}
	return s.githubClient().Available(s.ctx, s.flags["github"], s.flags["version"])
}

// gitClient uses normal local Git credentials, with existing GitHub login convenience only for GitHub sources.
func (s *session) gitClient() *gitpackages.Client {
	// Tests can select isolated artifact transport while exercising real local Git repositories.
	if s.options.Git != nil {
		return s.options.Git
	}
	client := gitpackages.New()
	repository := githubGitRepository(s.packageRepository())
	// Other Git providers use the caller's own credential helper or SSH configuration.
	if repository == "" {
		return client
	}
	token := githubpackages.Credentials(s.ctx)
	// Git itself uses its configured credential helper. API credentials apply only to matching release-asset URLs.
	client.HTTP.Transport = repositoryArtifactTransport{repository: repository, token: token, base: http.DefaultTransport}
	return client
}

// githubGitRepository recognizes only genuine GitHub source URLs for optional existing-login support.
func githubGitRepository(source string) string {
	repository := ""
	// Git's standard SSH shorthand has a fixed host boundary.
	if strings.HasPrefix(source, "git@github.com:") {
		repository = strings.TrimPrefix(source, "git@github.com:")
	} else {
		parsed, err := url.Parse(source)
		// Arbitrary host suffixes must never receive GitHub credentials.
		if err != nil || parsed.Host != "github.com" {
			return ""
		}
		repository = strings.TrimPrefix(parsed.Path, "/")
	}
	repository = strings.TrimSuffix(repository, ".git")
	// Public repository coordinates must still satisfy the provider's literal owner/name grammar.
	if githubpackages.ValidateSelection(repository, "HEAD") != nil {
		return ""
	}
	return repository
}

// repositoryArtifactTransport authenticates only GitHub asset API requests belonging to the selected Git source.
type repositoryArtifactTransport struct {
	repository, token string
	base              http.RoundTripper
}

// RoundTrip keeps private GitHub release support optional without leaking its token to generic artifact hosts.
func (t repositoryArtifactTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	// Repository credentials apply only to exact asset endpoints for that same source repository.
	if t.matches(request) && t.token != "" {
		request.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(request)
}

// matches rejects cross-origin redirects, arbitrary API routes and other repositories' asset identities.
func (t repositoryArtifactTransport) matches(request *http.Request) bool {
	// Only the trusted HTTPS API origin can receive this host's token.
	if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.URL.RawQuery != "" {
		return false
	}
	prefix := "/repos/" + t.repository + "/releases/assets/"
	// A manifest may not repurpose package access to another API surface.
	if !strings.HasPrefix(request.URL.Path, prefix) {
		return false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(request.URL.Path, prefix), 10, 64)
	return err == nil && id > 0
}
