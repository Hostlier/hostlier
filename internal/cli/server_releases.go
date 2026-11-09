package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// CI sets both values in the packaged CLI. Local builds use operator-supplied archives.
var serverArtifactRepository, serverArtifactCommit string

func (s *session) serverReleases(ctx context.Context, dir, arch string) (string, func(), error) {
	controller := "hostlier-controller-linux-" + arch
	_, err := os.Stat(filepath.Join(dir, controller+".tar.gz"))
	if s.flags["release-dir"] != "" || !os.IsNotExist(err) {
		return dir, func() {}, nil
	}
	hint := "place the matching Linux CLI and controller archives and checksums in " + dir + " or use --release-dir"
	if serverArtifactRepository == "" || serverArtifactCommit == "" {
		return "", nil, fmt.Errorf("%s", hint)
	}
	staging, err := os.MkdirTemp("", "hostlier-linux-release-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(staging) }
	for _, kind := range []string{"controller", "cli"} {
		artifact := "hostlier-" + kind + "-linux-" + arch
		fmt.Fprintf(s.errOut, "Fetching %s for CLI build %s…\n", artifact, serverArtifactCommit)
		if err := s.downloadServerArtifact(ctx, kind, artifact, staging); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("fetch %s: %w; %s", artifact, err, hint)
		}
	}
	return staging, cleanup, nil
}

// downloadServerArtifact accepts successful main-branch builds from this CLI's exact source revision.
func (s *session) downloadServerArtifact(ctx context.Context, kind, artifact, dir string) error {
	workflow := "hostlier-" + kind + ".yml"
	list := exec.CommandContext(ctx, "gh", "run", "list", "--repo", serverArtifactRepository, "--workflow", workflow, "--branch", "main", "--commit", serverArtifactCommit, "--status", "success", "--json", "databaseId,event")
	list.Stderr = s.errOut
	data, err := list.Output()
	if err != nil {
		return err
	}
	var runs []struct {
		DatabaseID int64
		Event      string
	}
	if err = json.Unmarshal(data, &runs); err != nil {
		return err
	}
	for _, run := range runs {
		if run.DatabaseID <= 0 || (run.Event != "push" && run.Event != "workflow_dispatch") {
			continue
		}
		download := exec.CommandContext(ctx, "gh", "run", "download", strconv.FormatInt(run.DatabaseID, 10), "--repo", serverArtifactRepository, "--name", artifact, "--dir", dir)
		download.Stderr = s.errOut
		return download.Run()
	}
	return fmt.Errorf("no matching successful %s build", workflow)
}
