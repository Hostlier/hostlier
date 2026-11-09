// Package gitpackages reads versioned package catalogs through the local Git transport.
package gitpackages

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
var scpPattern = regexp.MustCompile(`^(?:[A-Za-z0-9_][A-Za-z0-9_.-]*@)?[A-Za-z0-9][A-Za-z0-9.-]*:[A-Za-z0-9_./~-]+$`)

// ValidateSelection admits standard Git URLs and refs without embedded credentials or option injection.
func ValidateSelection(repository, ref string) error {
	// Source coordinates are displayed in approval records, so credentials and control characters are forbidden.
	if len(repository) > 200 || strings.ContainsAny(repository, "\x00\r\n\t ") {
		return fmt.Errorf("invalid Git repository URL")
	}
	// Empty refs mean HEAD for discovery only; installation checks for an explicit selection separately.
	if !validRef(ref) {
		return fmt.Errorf("invalid Git tag or commit")
	}
	// Git's common SSH shorthand is accepted without invoking a shell parser.
	if !strings.Contains(repository, "://") && scpPattern.MatchString(repository) {
		return nil
	}
	parsed, err := url.Parse(repository)
	// Opaque helpers, URL queries and fragments are not supported Git transport selections.
	if err != nil || !plainGitURL(parsed) {
		return fmt.Errorf("invalid Git repository URL")
	}
	return validateGitURL(parsed)
}

// validateGitURL accepts authenticated SSH, HTTPS and explicit local file repositories.
func validateGitURL(parsed *url.URL) error {
	// Transport schemes are selected explicitly rather than delegated to arbitrary remote helpers.
	switch parsed.Scheme {
	// Local Git mirrors require an absolute filesystem URL.
	case "file":
		// Local repositories must not redirect to a remote file host.
		if localGitURL(parsed) {
			return nil
		}
	// HTTPS authentication belongs in local credential helpers.
	case "https":
		// HTTPS URLs carry only public source coordinates.
		if parsed.User == nil && validGitHost(parsed) && parsed.Path != "" {
			return nil
		}
	// SSH supports public usernames and local host-key/agent configuration.
	case "ssh":
		// SSH syntax must remain a literal host, user and path.
		if validSSHURL(parsed) {
			return nil
		}
	}
	return fmt.Errorf("use an HTTPS, SSH, or file Git URL without embedded passwords")
}

// validRef excludes option injection and revision expressions from fetched source selections.
func validRef(ref string) bool {
	return ref == "" || (refPattern.MatchString(ref) && !strings.Contains(ref, ".."))
}

// plainGitURL excludes URL fragments, queries and opaque remote-helper syntax.
func plainGitURL(parsed *url.URL) bool {
	return parsed.Opaque == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

// localGitURL accepts only explicit absolute local repository URLs.
func localGitURL(parsed *url.URL) bool {
	return parsed.User == nil && (parsed.Host == "" || parsed.Host == "localhost") && strings.HasPrefix(parsed.Path, "/")
}

// validGitHost permits DNS names and numeric IP addresses without option-like host tokens.
func validGitHost(parsed *url.URL) bool {
	host := parsed.Hostname()
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`).MatchString(host) || net.ParseIP(host) != nil
}

// validSSHURL rejects decoded control characters, passwords and shell syntax in SSH usernames and paths.
func validSSHURL(parsed *url.URL) bool {
	// Remote Git paths are literal source coordinates rather than shell expressions.
	if !validGitHost(parsed) || !regexp.MustCompile(`^/[A-Za-z0-9_./~-]+$`).MatchString(parsed.Path) {
		return false
	}
	// The local SSH configuration can supply a username when the URL omits one.
	if parsed.User == nil {
		return true
	}
	_, password := parsed.User.Password()
	return !password && regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`).MatchString(parsed.User.Username())
}

// checkout holds a temporary bare repository and one resolved commit, never an executable checkout.
type checkout struct {
	directory, commit string
	environment       []string
}

// open fetches one explicit source selection using local credentials without checkout hooks or submodules.
func (c *Client) open(ctx context.Context, repository, ref string) (*checkout, error) {
	// Validate before creating files or invoking a transport helper.
	if err := ValidateSelection(repository, ref); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "hostlier-package-source-")
	// Temporary repository creation must precede all Git operations.
	if err != nil {
		return nil, err
	}
	repo := &checkout{directory: directory, environment: c.Environment}
	// Initialization and fetch errors must not retain a partially downloaded private repository.
	if err = repo.fetch(ctx, repository, ref); err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	return repo, nil
}

// fetch pins the advertised ref to a commit before reading any catalog or executable bytes.
func (r *checkout) fetch(ctx context.Context, repository, ref string) error {
	// A bare repository avoids working-tree filters and never initializes submodules.
	if _, err := r.run(ctx, 1024, "init", "--bare", r.directory); err != nil {
		return err
	}
	// Discovery follows remote HEAD; installation rejects omitted refs at the command boundary.
	if ref == "" {
		ref = "HEAD"
	}
	// Shallow fetch keeps transport work scoped to the selected version.
	if _, err := r.run(ctx, 1024, "fetch", "--depth=1", "--no-tags", "--", repository, ref); err != nil {
		return err
	}
	raw, err := r.run(ctx, 1024, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	// A resolved commit must be available before interpreting source contents.
	if err != nil {
		return err
	}
	r.commit = strings.TrimSpace(string(raw))
	// Git supports SHA-1 and SHA-256 object formats.
	if !regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`).MatchString(r.commit) {
		return fmt.Errorf("invalid Git commit identity")
	}
	return nil
}

// boundedBuffer prevents subprocess output from exceeding the requested blob or metadata bound.
type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int64
}

// Write refuses excess output rather than allocating unbounded subprocess data.
func (b *boundedBuffer) Write(data []byte) (int, error) {
	// The child cannot expand a manifest or binary beyond its protocol limit.
	if int64(b.buffer.Len())+int64(len(data)) > b.limit {
		return 0, fmt.Errorf("Git output exceeds limit")
	}
	return b.buffer.Write(data)
}

// run executes Git directly with bounded output and public errors that omit transport diagnostics.
func (r *checkout) run(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	options := []string{"--git-dir=" + r.directory, "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "-c", "protocol.file.allow=always"}
	command := exec.CommandContext(ctx, "git", append(options, args...)...)
	command.Env = gitEnvironment(r.environment)
	output := &boundedBuffer{limit: limit}
	command.Stdout = output
	command.Stderr = io.Discard
	// Missing refs and authentication failures must not disclose credentials or signed URLs.
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("Git %s failed; check repository access, revision, and catalog contents", args[0])
	}
	return output.buffer.Bytes(), nil
}

// gitEnvironment preserves local authentication while preventing inherited repository paths from escaping staging.
func gitEnvironment(environment []string) []string {
	// Production uses the caller's configured Git credentials and SSH agent.
	if environment == nil {
		environment = os.Environ()
	}
	result := []string{}
	excluded := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_CONFIG": true}
	// Repository-location variables must not redirect any temporary-repository command.
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		// Transport settings and normal credential helpers remain available locally.
		if !excluded[key] {
			result = append(result, entry)
		}
	}
	return append(result, "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
}

// blob reads only an ordinary tracked blob from the resolved commit, never a symlink or submodule.
func (r *checkout) blob(ctx context.Context, path string, limit int64) ([]byte, error) {
	raw, err := r.run(ctx, 2048, "ls-tree", "-z", r.commit, "--", path)
	// Missing or nonregular entries cannot redirect catalog or artifact reads.
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(raw))
	// Only regular file modes may supply package content.
	if len(fields) < 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
		return nil, fmt.Errorf("package source requires an ordinary tracked file")
	}
	return r.run(ctx, limit, "show", r.commit+":"+path)
}
