package cli

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"hostlier/internal/model"
)

// parse separates positionals and recognized flags while rejecting malformed options.
func parse(args []string) (arguments, error) {
	result := arguments{flags: map[string]string{}}
	booleans := map[string]bool{"tty": true, "detect": true, "ui": true, "ssh-sudo": true, "global": true, "force": true, "yes": true, "dry-run": true, "json": true, "toon": true, "stdin": true, "token-stdin": true, "request": true, "follow": true, "detach": true, "purge": true, "keep-apps": true, "help": true, "watch": true, "source-stopped": true, "all": true}
	values := map[string]bool{}
	// Register value-taking options so they cannot be mistaken for booleans.
	for _, v := range strings.Fields("backup-dir release-dir package compose dependencies architecture context service bridge-id connections engine-url client-id client-secret-ref family-id secret-name allow repository github version directory resource destination every retain to image before limit tail ssh ssh-config ssh-socket email auth auth-config data-config key public-key app app-prefix max-apps route-domain scope for device max-uses secrets create-secrets on url fingerprint controller-fingerprint target file route server socket name from-env domain runtime controller-hostname state-dir config port host hostname log-path operation revision storage output backup cursor input cpu memory replicas proxy component") {
		values[v] = true
	}
	// Consume each command token once while retaining positional order.
	for i := 0; i < len(args); i++ {
		value := args[i]
		// Only native SSH accepts an opaque command tail; its flags never affect Hostlier.
		if value == "--" && len(result.args) >= 2 && result.args[0] == "server" && result.args[1] == "ssh" {
			result.remoteArgs = append([]string{}, args[i+1:]...)
			break
		}
		// Support the conventional short help flag at every command level.
		if value == "-h" {
			value = "--help"
		}
		// Keep non-option tokens as command positionals.
		if !strings.HasPrefix(value, "--") {
			result.args = append(result.args, value)
			continue
		}
		key := strings.TrimPrefix(value, "--")
		// Reject duplicate flags so a later value cannot silently override earlier intent.
		if _, ok := result.flags[key]; ok {
			return result, model.Err("ARGUMENTS", "Duplicate --"+key)
		}
		// Recognized boolean flags do not consume a positional value.
		if booleans[key] {
			result.flags[key] = "true"
			// Consume a following argument only for options requiring values.
		} else if values[key] {
			// Require a value immediately after each value-taking flag.
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return result, model.Err("ARGUMENTS", "--"+key+" requires a value")
			}
			i++
			result.flags[key] = args[i]
		} else {
			return result, model.Err("ARGUMENTS", "Unknown option --"+key)
		}
	}
	// The UI template switch belongs to project creation.
	if result.flags["ui"] != "" && !supportsUIInit(result) {
		return result, model.Err("ARGUMENTS", "--ui requires hostlier init --package NAME")
	}
	return result, nil
}

// readInput bounds stdin consumption to prevent unbounded secret input.
func readInput(in io.Reader) (string, error) {
	data, e := io.ReadAll(io.LimitReader(in, 65537))
	// Reject this path: Cannot read secret input.
	if e != nil {
		return "", model.Err("INPUT", "Cannot read secret input")
	}
	// Reject this path: Input exceeds 65536 byte limit.
	if len(data) > 65536 {
		return "", model.Err("INPUT_LIMIT", "Input exceeds 65536 byte limit")
	}
	return string(data), nil
}

// terminal requires terminal-backed input and output before offering interactive approval.
func terminal(in io.Reader, out io.Writer) bool {
	input, ok := in.(*os.File)
	// Reject streams that cannot support an interactive terminal prompt.
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return false
	}
	output, ok := out.(*os.File)
	return ok && term.IsTerminal(int(output.Fd()))
}

// supportsUIInit confines template selection to an explicit package initializer.
func supportsUIInit(args arguments) bool {
	return len(args.args) == 1 && args.args[0] == "init" && args.flags["package"] != ""
}
