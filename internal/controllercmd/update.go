package controllercmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hostlier/internal/collectorimage"
	"hostlier/internal/config"
	"hostlier/internal/installer"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	controllerRepository = "Hostlier/hostlier"
	githubAPI            = "https://api.github.com/repos/" + controllerRepository
	maxArchiveBytes      = 512 << 20
	maxExtractedBytes    = 1024 << 20
)

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

// githubAsset is trusted only after the official API and every asset coordinate are checked.
type githubAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

// githubRelease describes a published version from the fixed official repository.
type githubRelease struct {
	ID         int64         `json:"id"`
	URL        string        `json:"url"`
	HTMLURL    string        `json:"html_url"`
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

// updateOptions contains only local installation paths and explicit approval choices.
type updateOptions struct {
	stateDir, configPath string
	dry, yes             bool
}

// updateClient keeps network trust and timeout policy in one place.
func updateClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" {
			return errors.New("untrusted GitHub download redirect")
		}
		switch req.URL.Hostname() {
		case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		default:
			return errors.New("untrusted GitHub download host")
		}
	}}
}

// runUpdate downloads a verified complete bundle and delegates all host changes to init.
func runUpdate(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	options, err := parseUpdateOptions(args, errOut)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err = requireUpdateHost(options.stateDir); err != nil {
		return err
	}
	installed, domain, err := installedUpdateTarget(options.stateDir, options.configPath)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(options.stateDir, ".controller-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	client := updateClient()
	fmt.Fprintln(errOut, "Checking the official Hostlier repository and latest stable release…")
	if err = verifyOfficialRepository(ctx, client); err != nil {
		return err
	}
	release, err := latestControllerRelease(ctx, client, runtime.GOARCH)
	if err != nil {
		return err
	}
	fmt.Fprintf(errOut, "Downloading and checking %s for Linux %s…\n", release.TagName, runtime.GOARCH)
	archive, err := downloadControllerBundle(ctx, client, release, runtime.GOARCH, stage)
	if err != nil {
		return err
	}
	fmt.Fprintln(errOut, "Extracting the verified controller and collector image…")
	if err = extractControllerBundle(archive, stage); err != nil {
		return err
	}
	fmt.Fprintf(errOut, "Verified %s from github.com/%s (SHA-256: %s)\n", release.TagName, controllerRepository, releaseAssetDigest(release, runtime.GOARCH))
	return startVerifiedInit(ctx, filepath.Join(stage, "hostlier-controller"), installed, domain, options, in, out, errOut)
}

// parseUpdateOptions rejects extra arguments before opening privileged state.
func parseUpdateOptions(args []string, errOut io.Writer) (updateOptions, error) {
	flags := flag.NewFlagSet("hostlier-controller update", flag.ContinueOnError)
	flags.SetOutput(errOut)
	stateDir := flags.String("state-dir", "/var/lib/hostlier", "installed controller state directory")
	configPath := flags.String("config", "/etc/hostlier/config.yaml", "installed controller configuration")
	dry := flags.Bool("dry-run", false, "verify the release and preview installation only")
	yes := flags.Bool("yes", false, "approve the reviewed installation plan")
	if err := flags.Parse(args); err != nil {
		return updateOptions{}, err
	}
	if flags.NArg() != 0 {
		return updateOptions{}, errors.New("update accepts flags only")
	}
	return updateOptions{stateDir: *stateDir, configPath: *configPath, dry: *dry, yes: *yes}, nil
}

// requireUpdateHost accepts only root-owned private controller state on supported Linux hosts.
func requireUpdateHost(stateDir string) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" || os.Geteuid() != 0 {
		return errors.New("update requires root on a supported Linux amd64 or arm64 host")
	}
	return protectedUpdatePath(stateDir)
}

// protectedUpdatePath rejects symlinked or writable ancestors before executing staged root code.
func protectedUpdatePath(stateDir string) error {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return errors.New("controller state directory must be a clean absolute path")
	}
	for current := stateDir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("controller state directory and ancestors must be root-owned and not writable by other users")
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}

// startVerifiedInit reuses the release's installer after complete bundle verification.
func startVerifiedInit(ctx context.Context, executable string, installed installer.Installation, domain string, options updateOptions, in io.Reader, out, errOut io.Writer) error {
	initArgs := []string{"init", "--domain", domain, "--controller-hostname", installed.ControllerHostname, "--runtime", installed.Runtime, "--proxy", installed.Proxy, "--state-dir", options.stateDir, "--config", options.configPath}
	if installed.MasterFingerprint != "" {
		initArgs = append(initArgs, "--master-fingerprint", installed.MasterFingerprint)
	}
	if options.dry {
		initArgs = append(initArgs, "--dry-run")
	} else if options.yes {
		initArgs = append(initArgs, "--yes")
	}
	command := exec.CommandContext(ctx, executable, initArgs...)
	command.Stdin, command.Stdout, command.Stderr = in, out, errOut
	command.Env = withoutGitHubTokens(os.Environ())
	return command.Run()
}

// withoutGitHubTokens keeps release credentials out of the installer process.
func withoutGitHubTokens(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "GH_TOKEN=") && !strings.HasPrefix(entry, "GITHUB_TOKEN=") {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// installedUpdateTarget preserves the existing hostname, proxy and runtime on upgrade.
func installedUpdateTarget(stateDir, configPath string) (installer.Installation, string, error) {
	var installed installer.Installation
	data, err := os.ReadFile(filepath.Join(stateDir, "installation.json"))
	if err != nil {
		return installed, "", fmt.Errorf("read installed controller: %w", err)
	}
	if err = json.Unmarshal(data, &installed); err != nil || !installed.Ready || installed.ControllerHostname == "" || installed.Runtime == "" || installed.Proxy == "" {
		return installed, "", errors.New("controller installation is incomplete or invalid; repair it with the release bundle first")
	}
	server, err := config.ReadServer(configPath)
	if err != nil || len(server.Domains) == 0 {
		return installed, "", errors.New("cannot read the installed application domain")
	}
	return installed, server.Domains[0], nil
}

// githubJSON fetches bounded metadata exclusively from the fixed official API.
func githubJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := githubToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("official GitHub API returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}

// githubToken supports private official releases without sending credentials to other hosts.
func githubToken() string {
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("GITHUB_TOKEN")
}

// verifyOfficialRepository rejects a different repository identity at its fixed API path.
func verifyOfficialRepository(ctx context.Context, client *http.Client) error {
	var repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	}
	if err := githubJSON(ctx, client, githubAPI, &repository); err != nil {
		return err
	}
	if repository.FullName != controllerRepository || repository.HTMLURL != "https://github.com/"+controllerRepository {
		return errors.New("GitHub repository identity does not match the official Hostlier source")
	}
	return nil
}

// latestControllerRelease accepts only a stable release and both exact architecture assets.
func latestControllerRelease(ctx context.Context, client *http.Client, arch string) (githubRelease, error) {
	var release githubRelease
	if err := githubJSON(ctx, client, githubAPI+"/releases/latest", &release); err != nil {
		return release, fmt.Errorf("find latest Hostlier release: %w", err)
	}
	if !releaseTag.MatchString(release.TagName) || release.Draft || release.Prerelease || release.ID <= 0 || !checkedGitHubURL(release.HTMLURL, "github.com", "/"+controllerRepository+"/releases/tag/"+release.TagName) {
		return release, errors.New("latest release does not match the official stable Hostlier release format")
	}
	if !checkedGitHubURL(release.URL, "api.github.com", "/repos/"+controllerRepository+"/releases/"+fmt.Sprint(release.ID)) {
		return release, errors.New("release API source does not match the official repository")
	}
	name := controllerArchiveName(arch)
	for _, wanted := range []string{name, name + ".sha256"} {
		if _, err := checkedReleaseAsset(release, wanted); err != nil {
			return release, err
		}
	}
	return release, nil
}

// checkedReleaseAsset binds the asset name, API coordinate and GitHub digest.
func checkedReleaseAsset(release githubRelease, name string) (githubAsset, error) {
	var selected githubAsset
	for _, asset := range release.Assets {
		if asset.Name == name {
			if selected.Name != "" {
				return selected, errors.New("duplicate Hostlier release asset")
			}
			selected = asset
		}
	}
	if selected.Name == "" || selected.ID <= 0 || !checkedGitHubURL(selected.URL, "api.github.com", "/repos/"+controllerRepository+"/releases/assets/"+fmt.Sprint(selected.ID)) || !checkedGitHubURL(selected.BrowserDownloadURL, "github.com", "/"+controllerRepository+"/releases/download/"+release.TagName+"/"+name) || !validSHA256Digest(selected.Digest) || selected.Size <= 0 {
		return selected, fmt.Errorf("missing or invalid official release asset %s", name)
	}
	return selected, nil
}

// controllerArchiveName selects the complete server bundle, including collector-image.
func controllerArchiveName(arch string) string {
	return "hostlier-controller-linux-" + arch + ".tar.gz"
}

// releaseAssetDigest supplies the verified digest for the progress message.
func releaseAssetDigest(release githubRelease, arch string) string {
	asset, _ := checkedReleaseAsset(release, controllerArchiveName(arch))
	return strings.TrimPrefix(asset.Digest, "sha256:")
}

// validSHA256Digest requires the release API's full unambiguous digest format.
func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// downloadControllerBundle checks both GitHub asset digests and the published SHA file.
func downloadControllerBundle(ctx context.Context, client *http.Client, release githubRelease, arch, stage string) (string, error) {
	name := controllerArchiveName(arch)
	checksum, err := checkedReleaseAsset(release, name+".sha256")
	if err != nil {
		return "", err
	}
	checksumPath := filepath.Join(stage, checksum.Name)
	if err = fetchReleaseAsset(ctx, client, checksum, checksumPath, 4096); err != nil {
		return "", err
	}
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		return "", err
	}
	want, err := parseReleaseChecksum(data, name)
	if err != nil {
		return "", err
	}
	archive, err := checkedReleaseAsset(release, name)
	if err != nil {
		return "", err
	}
	if archive.Digest != "sha256:"+want {
		return "", errors.New("GitHub asset digest and published SHA-256 file disagree")
	}
	archivePath := filepath.Join(stage, name)
	if err = fetchReleaseAsset(ctx, client, archive, archivePath, maxArchiveBytes); err != nil {
		return "", err
	}
	return archivePath, nil
}

// parseReleaseChecksum accepts only the exact archive named by the release.
func parseReleaseChecksum(data []byte, name string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) != 2 || fields[1] != name || len(fields[0]) != 64 {
		return "", errors.New("invalid Hostlier SHA-256 file")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", errors.New("invalid Hostlier SHA-256 value")
	}
	return strings.ToLower(fields[0]), nil
}

// fetchReleaseAsset streams a bounded asset from the official API and verifies its digest.
func fetchReleaseAsset(ctx context.Context, client *http.Client, asset githubAsset, destination string, limit int64) error {
	if asset.Size > limit {
		return errors.New("Hostlier release asset exceeds its size limit")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := githubToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub asset download returned HTTP %d", response.StatusCode)
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(response.Body, limit+1))
	if err != nil || !verifiedAssetBytes(asset, size, limit, hasher.Sum(nil)) {
		return errors.New("GitHub release asset failed size or SHA-256 verification")
	}
	return out.Sync()
}

// verifiedAssetBytes requires the advertised length and exact release API digest.
func verifiedAssetBytes(asset githubAsset, size, limit int64, digest []byte) bool {
	return size == asset.Size && size <= limit && "sha256:"+hex.EncodeToString(digest) == asset.Digest
}

// extractControllerBundle admits only regular files beneath the two expected release entries.
func extractControllerBundle(archive, stage string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	seen := map[string]bool{}
	var total int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err = extractControllerEntry(tarReader, header, stage, seen, &total); err != nil {
			return err
		}
	}
	if !seen["hostlier-controller"] {
		return errors.New("controller release archive has no executable")
	}
	image, err := collectorimage.Open(filepath.Join(stage, "collector-image"))
	if err != nil {
		return fmt.Errorf("controller release has an invalid collector image: %w", err)
	}
	return image.Close()
}

// extractControllerEntry rejects traversal, links, duplicates and unbounded expansion.
func extractControllerEntry(reader io.Reader, header *tar.Header, stage string, seen map[string]bool, total *int64) error {
	clean := path.Clean(header.Name)
	if !safeControllerEntryName(header.Name, clean) || seen[clean] {
		return errors.New("unsafe or duplicate controller archive entry")
	}
	seen[clean] = true
	if header.Typeflag == tar.TypeDir {
		return os.MkdirAll(filepath.Join(stage, clean), 0700)
	}
	if header.Typeflag != tar.TypeReg || !validExtractedSize(header.Size, *total) {
		return errors.New("unsupported or oversized controller archive entry")
	}
	*total += header.Size
	return extractControllerFile(reader, filepath.Join(stage, clean), header.Size, clean == "hostlier-controller")
}

// safeControllerEntryName confines members to the exact release layout.
func safeControllerEntryName(name, clean string) bool {
	return (clean == name || clean+"/" == name) && (clean == "hostlier-controller" || clean == "collector-image" || strings.HasPrefix(clean, "collector-image/"))
}

// validExtractedSize prevents individual and aggregate archive expansion attacks.
func validExtractedSize(size, total int64) bool {
	return size >= 0 && size <= maxArchiveBytes && total <= maxExtractedBytes-size
}

// extractControllerFile creates one private file after its archive path was validated.
func extractControllerFile(reader io.Reader, destination string, size int64, executable bool) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = io.CopyN(file, reader, size); err != nil {
		return err
	}
	if executable {
		if err = file.Chmod(0700); err != nil {
			return err
		}
	}
	return file.Sync()
}

// checkedGitHubURL rejects release metadata that points outside the official origin.
func checkedGitHubURL(raw, expectedHost, expectedPath string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host == expectedHost && parsed.Path == expectedPath && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil
}
