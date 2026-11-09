package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"hostlier/internal/model"
)

const CaddyBinaryVersion = "2.11.8"

var caddyArchiveChecksums = map[string]string{
	"amd64": "f032f8805e4df2ce5e1a8a624495c91e9ff763d8ff3ceddadd4784a07c472f40",
	"arm64": "6781a68e090fecda8c563b5a3b00115e18d7134084099ae804a017622471bed1",
}

// installCaddyBinary adds a verified Hostlier executable without replacing the distro package.
func installCaddyBinary(ctx context.Context, owned bool) error {
	info, err := os.Lstat(model.CaddyBinaryPath)
	if err == nil {
		if !owned || !info.Mode().IsRegular() {
			return errors.New("existing Hostlier Caddy executable requires ownership review")
		}
		if version, ok := command(ctx, model.CaddyBinaryPath, "version"); ok && strings.HasPrefix(version, "v"+CaddyBinaryVersion+" ") {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	checksum, ok := caddyArchiveChecksums[runtime.GOARCH]
	if !ok {
		return errors.New("unsupported Caddy architecture")
	}
	asset := "caddy_" + CaddyBinaryVersion + "_linux_" + runtime.GOARCH + ".tar.gz"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/caddyserver/caddy/releases/download/v"+CaddyBinaryVersion+"/"+asset, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 3 * time.Minute}).Do(request)
	if err != nil {
		return errors.New("cannot download pinned Caddy archive")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("pinned Caddy archive is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if err != nil || len(data) > 32<<20 {
		return errors.New("pinned Caddy archive exceeded its limit")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != checksum {
		return errors.New("pinned Caddy archive checksum verification failed")
	}
	return saveCaddyArchive(data, model.CaddyBinaryPath)
}

// saveCaddyArchive extracts only the executable from the verified, size-bounded archive.
func saveCaddyArchive(data []byte, target string) error {
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	var staged string
	defer func() {
		if staged != "" {
			os.Remove(staged)
		}
	}()
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Size < 0 || header.Size > 128<<20 {
			return errors.New("Caddy archive entry exceeds its limit")
		}
		if header.Name != "caddy" {
			continue
		}
		if staged != "" || header.Typeflag != tar.TypeReg || header.Size == 0 {
			return errors.New("Caddy archive contains an invalid executable")
		}
		file, err := os.CreateTemp(filepath.Dir(target), ".hostlier-caddy-*")
		if err != nil {
			return err
		}
		staged = file.Name()
		written, copyErr := io.Copy(file, archive)
		if copyErr == nil && written != header.Size {
			copyErr = fmt.Errorf("incomplete Caddy executable")
		}
		if copyErr == nil {
			copyErr = file.Chmod(0755)
		}
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if staged == "" {
		return errors.New("Caddy archive contains no executable")
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}
	staged = ""
	return nil
}
