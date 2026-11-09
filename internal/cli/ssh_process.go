package cli

import (
	"bytes"
	"context"
	"hostlier/internal/model"
	"os/exec"
)

// bootstrapOutput bounds untrusted SSH stdout before any JSON decoding or browser launch.
type bootstrapOutput struct{ data bytes.Buffer }

// Write aborts oversized protocol output without retaining its secret contents in errors.
func (b *bootstrapOutput) Write(p []byte) (int, error) {
	// A banner or compromised remote process cannot exhaust local memory.
	if b.data.Len()+len(p) > 65536 {
		return 0, model.Err("SSH_BOOTSTRAP", "SSH bootstrap output exceeded its limit")
	}
	return b.data.Write(p)
}

// runBootstrapSSH invokes native SSH using the user's identity and explicit confirmation of new host keys.
func (s *session) runBootstrapSSH(ctx context.Context, args []string) ([]byte, error) {
	var output bootstrapOutput
	command := exec.CommandContext(ctx, "ssh", args...)
	command.Stdin, command.Stdout, command.Stderr = s.in, &output, s.errOut
	// Native SSH reports host-key, identity and remote permission errors directly to the terminal.
	if err := command.Run(); err != nil {
		// Cancellation must stop SSH promptly and leave profile state untouched.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, model.Err("SSH_BOOTSTRAP", "SSH owner setup failed; check SSH access, the hostlier-controller version and socket permissions (use --ssh-sudo if required)")
	}
	return output.data.Bytes(), nil
}
