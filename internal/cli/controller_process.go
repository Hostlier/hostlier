package cli

import (
	"os"
	"os/exec"
	"time"

	"hostlier/internal/controllerbinary"
	"hostlier/internal/model"
)

// controllerProcess preserves local command aliases without linking server implementation into the CLI.
func (s *session) controllerProcess() error {
	// The controller executable formats its own output and has no TOON encoder.
	if s.flags["toon"] != "" {
		return model.Err("ARGUMENTS", "--toon is not supported for controller commands; use --json")
	}
	executable, err := controllerbinary.Resolve()
	// Resolve the installed server before starting a privileged local operation.
	if err != nil {
		return err
	}
	command := exec.CommandContext(s.ctx, executable, s.controllerArguments()...)
	command.Stdin, command.Stdout, command.Stderr = s.in, s.out, s.errOut
	// Forward cancellation gracefully so the daemon can flush and release its state lock.
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 25 * time.Second
	return command.Run()
}

// controllerArguments reconstructs the already validated invocation as literal process arguments.
func (s *session) controllerArguments() []string {
	args := append([]string(nil), s.parsed.args...)
	// Boolean switches do not consume a following argument.
	for key, value := range s.flags {
		args = append(args, "--"+key)
		// Restore assertions and JSON output selection are boolean switches.
		if key != "source-stopped" && key != "json" {
			args = append(args, value)
		}
	}
	return args
}
