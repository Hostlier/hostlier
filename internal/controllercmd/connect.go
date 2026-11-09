package controllercmd

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"hostlier/internal/model"
)

// An optional sibling CLI owns browser pairing, credentials and master approval.
func connectMaster(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	executable, err := initExecutable("")
	if err != nil {
		return err
	}
	client := filepath.Join(filepath.Dir(executable), "hostlier")
	if _, err := os.Stat(client); err != nil {
		if os.IsNotExist(err) {
			return model.Err("CLI_REQUIRED", "The optional hostlier CLI is not installed beside the controller; run hostlier connect from your laptop or install the CLI separately")
		}
		return err
	}
	command := exec.CommandContext(ctx, client, append([]string{"server", "join"}, args...)...)
	command.Stdin, command.Stdout, command.Stderr = in, out, errOut
	return command.Run()
}
