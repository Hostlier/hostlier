package controllercmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hostlier/internal/collectorimage"
	"hostlier/internal/installer"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateTransport supplies official API fixtures without opening a network socket.
type updateTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements the small fixture transport used by release verification tests.
func (transport updateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// updateResponse builds a bounded response with the exact bytes under test.
func updateResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
}

// updateFixtureAsset keeps the API, browser and digest identities consistent.
func updateFixtureAsset(name string, id int64, body []byte) githubAsset {
	sum := sha256.Sum256(body)
	return githubAsset{ID: id, Name: name, URL: fmt.Sprintf("%s/releases/assets/%d", githubAPI, id), BrowserDownloadURL: "https://github.com/" + controllerRepository + "/releases/download/v1.2.3/" + name, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(body))}
}

// updateFixtureRelease describes one stable release and its complete AMD64 bundle.
func updateFixtureRelease(archive []byte) (githubRelease, []byte) {
	name := controllerArchiveName("amd64")
	sum := sha256.Sum256(archive)
	checksum := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	return githubRelease{ID: 42, URL: githubAPI + "/releases/42", HTMLURL: "https://github.com/" + controllerRepository + "/releases/tag/v1.2.3", TagName: "v1.2.3", Assets: []githubAsset{updateFixtureAsset(name, 10, archive), updateFixtureAsset(name+".sha256", 11, checksum)}}, checksum
}

// TestUpdateRejectsForeignReleaseMetadata checks the repository and exact asset URLs.
func TestUpdateRejectsForeignReleaseMetadata(t *testing.T) {
	release, _ := updateFixtureRelease([]byte("archive"))
	data, _ := json.Marshal(release)
	client := &http.Client{Transport: updateTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == githubAPI {
			return updateResponse([]byte(`{"full_name":"Hostlier/hostlier","html_url":"https://github.com/Hostlier/hostlier"}`)), nil
		}
		return updateResponse(data), nil
	})}
	if err := verifyOfficialRepository(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if _, err := latestControllerRelease(context.Background(), client, "amd64"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*githubRelease){
		func(r *githubRelease) { r.HTMLURL = "https://github.com/other/hostlier/releases/tag/v1.2.3" },
		func(r *githubRelease) {
			r.Assets[0].URL = "https://api.github.com/repos/other/hostlier/releases/assets/10"
		},
		func(r *githubRelease) {
			r.Assets[0].BrowserDownloadURL = "https://github.com/other/hostlier/releases/download/v1.2.3/hostlier-controller-linux-amd64.tar.gz"
		},
		func(r *githubRelease) { r.Assets[0].Digest = "" },
	} {
		candidate := release
		candidate.Assets = append([]githubAsset(nil), release.Assets...)
		change(&candidate)
		data, _ = json.Marshal(candidate)
		if _, err := latestControllerRelease(context.Background(), client, "amd64"); err == nil {
			t.Fatal("foreign or incomplete release was accepted", candidate)
		}
	}
}

// TestUpdateRequiresTwoMatchingChecksums rejects a modified archive before extraction.
func TestUpdateRequiresTwoMatchingChecksums(t *testing.T) {
	archive := []byte("complete-bundle")
	release, checksum := updateFixtureRelease(archive)
	client := &http.Client{Transport: updateTransport(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/11") {
			return updateResponse(checksum), nil
		}
		return updateResponse(archive), nil
	})}
	if _, err := downloadControllerBundle(context.Background(), client, release, "amd64", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	release.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := downloadControllerBundle(context.Background(), client, release, "amd64", t.TempDir()); err == nil {
		t.Fatal("mismatched API and SHA file digests were accepted")
	}
	if _, err := parseReleaseChecksum([]byte(strings.Repeat("a", 64)+"  other.tar.gz\n"), controllerArchiveName("amd64")); err == nil {
		t.Fatal("checksum for another asset was accepted")
	}
}

// updateArchive creates the exact bundle layout used by scripts/release.sh.
func updateArchive(t *testing.T, extra string) []byte {
	t.Helper()
	root, image := updateFixtureImage(t)
	var data bytes.Buffer
	zip := gzip.NewWriter(&data)
	archive := tar.NewWriter(zip)
	if err := addUpdateEntry(archive, "hostlier-controller", []byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := addUpdateImage(archive, root, image); err != nil {
		t.Fatal(err)
	}
	if extra != "" {
		if err := addUpdateEntry(archive, extra, []byte("escape")); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

// updateFixtureImage makes a valid small OCI layout for archive verification.
func updateFixtureImage(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{"collector": "collector", "ca.pem": "public CA"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	image := filepath.Join(root, "collector-image")
	if err := collectorimage.Build(image, []collectorimage.PlatformInput{{Architecture: "amd64", BinaryPath: filepath.Join(root, "collector"), CAPath: filepath.Join(root, "ca.pem")}}); err != nil {
		t.Fatal(err)
	}
	return root, image
}

// addUpdateImage copies only the generated OCI files into the test archive.
func addUpdateImage(archive *tar.Writer, root, image string) error {
	return filepath.Walk(image, func(name string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		return addUpdateEntry(archive, filepath.ToSlash(relative), body)
	})
}

// addUpdateEntry writes one regular archive member with controlled metadata.
func addUpdateEntry(writer *tar.Writer, name string, body []byte) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := writer.Write(body)
	return err
}

// TestUpdateExtractsOnlyVerifiedLayout checks collector validation and traversal refusal.
func TestUpdateExtractsOnlyVerifiedLayout(t *testing.T) {
	for _, test := range []struct {
		name, extra string
		valid       bool
	}{{name: "complete", valid: true}, {name: "escape", extra: "../outside"}, {name: "foreign", extra: "other-program"}} {
		t.Run(test.name, func(t *testing.T) {
			stage := t.TempDir()
			archive := filepath.Join(stage, "bundle.tar.gz")
			if err := os.WriteFile(archive, updateArchive(t, test.extra), 0600); err != nil {
				t.Fatal(err)
			}
			err := extractControllerBundle(archive, stage)
			if (err == nil) != test.valid {
				t.Fatalf("unexpected extraction result: %v", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(stage), "outside")); !os.IsNotExist(err) {
				t.Fatal("archive escaped the private staging directory")
			}
		})
	}
}

// TestUpdateKeepsInstalledConfiguration checks the updater's existing server selection.
func TestUpdateKeepsInstalledConfiguration(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	installed := installer.Installation{Ready: true, Runtime: "docker", Proxy: "nginx-existing", ControllerHostname: "ops.example.com"}
	data, _ := json.Marshal(installed)
	if err := os.WriteFile(filepath.Join(state, "installation.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\ndomains:\n  - example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	actual, domain, err := installedUpdateTarget(state, configPath)
	if err != nil || actual.ControllerHostname != installed.ControllerHostname || actual.Proxy != installed.Proxy || actual.Runtime != installed.Runtime || domain != "example.com" {
		t.Fatalf("installed update target changed: %+v %q %v", actual, domain, err)
	}
}

// TestUpdateDoesNotForwardGitHubTokens keeps private release access out of init.
func TestUpdateDoesNotForwardGitHubTokens(t *testing.T) {
	actual := withoutGitHubTokens([]string{"PATH=/usr/bin", "GH_TOKEN=secret", "GITHUB_TOKEN=secret", "HOME=/root"})
	if strings.Join(actual, ",") != "PATH=/usr/bin,HOME=/root" {
		t.Fatal("GitHub release credentials reached the installer", actual)
	}
}

// TestUpdateRedirectsKeepTokensOnGitHubAPI prevents release credentials reaching asset hosts.
func TestUpdateRedirectsKeepTokensOnGitHubAPI(t *testing.T) {
	requests := 0
	client := updateClient()
	client.Transport = updateTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://release-assets.githubusercontent.com/signed-bundle"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("release token reached a redirect destination")
		}
		return updateResponse([]byte("bundle")), nil
	})
	request, _ := http.NewRequest(http.MethodGet, githubAPI+"/releases/assets/10", nil)
	request.Header.Set("Authorization", "Bearer private-token")
	response, err := client.Do(request)
	if err != nil || requests != 2 {
		t.Fatalf("trusted redirect failed: requests=%d err=%v", requests, err)
	}
	response.Body.Close()
	client.Transport = updateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://example.invalid/bundle"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := client.Do(request); err == nil {
		t.Fatal("untrusted release redirect was followed")
	}
}

// TestUpdateRejectsUnsafeStateAncestry keeps root execution away from user-swappable paths.
func TestUpdateRejectsUnsafeStateAncestry(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative-state", state + "/../state", link} {
		if err := protectedUpdatePath(path); err == nil {
			t.Fatal("unsafe controller update path accepted", path)
		}
	}
}
