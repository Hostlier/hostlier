package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/githubpackages"
	"hostlier/internal/gitpackages"
	"hostlier/internal/model"
	bundled "hostlier/internal/skills"
)

// Requester abstracts authenticated API requests for production and injected clients.
type Requester interface {
	// Request carries one authenticated controller operation over the selected transport.
	Request(context.Context, string, string, any, any) error
}

// Options configures runtime dependencies, persistence and bounded controller deadlines.
type Options struct {
	GitHub       *githubpackages.Client
	Git          *gitpackages.Client
	InstallSDK   func(context.Context, string, string, string) error
	RunMinion    func(context.Context, string, string, []string, io.Reader, io.Writer, io.Writer) error
	Prompt       func(string, bool) (string, error)
	OpenBrowser  func(string) error
	RunSSH       func(context.Context, []string) ([]byte, error)
	Client       Requester
	Home, CWD    string
	Environment  map[string]string
	PollInterval time.Duration
}

// arguments keeps parsed positional arguments separate from flags.
type arguments struct {
	remoteArgs  []string
	commandPath string
	args        []string
	flags       map[string]string
}

// session owns only the state of one CLI invocation.
type session struct {
	ctx                              context.Context
	parsed                           arguments
	flags                            map[string]string
	command, sub, target             string
	in                               io.Reader
	out, errOut                      io.Writer
	options                          Options
	interactive                      bool
	reader                           *bufio.Reader
	store                            client.Store
	cwd, serverTarget, masterProfile string
	remote                           Requester
	projectSecrets                   []projectSecret
	containerArchitecture            string
	// format is human, json or toon for this invocation's results.
	format string
}

// newSession binds parsing, streams and dependencies without network or server mutations.
func newSession(ctx context.Context, parsed arguments, in io.Reader, out, errOut io.Writer, options Options) *session {
	s := &session{ctx: ctx, parsed: parsed, flags: parsed.flags, in: in, out: out, errOut: errOut, options: options, interactive: options.Prompt != nil || terminal(in, errOut), reader: bufio.NewReader(in), store: client.NewStore(options.Home), cwd: options.CWD}
	s.command, s.sub, s.target = s.position(0), s.position(1), s.position(2)
	s.format = outputFormat(parsed.flags, out)
	aliases := map[string]string{"domain": "domains", "client": "clients"}
	// Normalize supported aliases before command dispatch.
	if alias := aliases[s.command]; alias != "" {
		s.command = alias
	}
	// Use the process working directory when no override was injected.
	if s.cwd == "" {
		s.cwd, _ = os.Getwd()
	}
	return s
}

// position safely reads an optional positional argument.
func (s *session) position(i int) string {
	// Read an optional positional only when its index exists.
	if i < len(s.parsed.args) {
		return s.parsed.args[i]
	}
	return ""
}

// print writes one result to command output in the selected format.
func (s *session) print(value any) error { return s.write(s.out, value, false) }

// message writes a human-readable status line to command output.
func (s *session) message(value string) { fmt.Fprintln(s.out, value) }

// ask uses the injected prompt or terminal reader with the requested echo behavior.
func (s *session) ask(question string, hidden bool) (string, error) {
	// Reject this path: Interactive terminal required; use an explicit stdin input mode where supported.
	if !s.interactive {
		return "", model.Err("INTERACTIVE_REQUIRED", "Interactive terminal required; use an explicit stdin input mode where supported")
	}
	// Use the injected prompt without manipulating a real terminal.
	if s.options.Prompt != nil {
		return s.options.Prompt(question, hidden)
	}
	return promptTerminal(s.ctx, s.in.(*os.File), s.reader, s.errOut, question, hidden)
}

// affirmative accepts only explicit affirmative answers.
func affirmative(answer string) bool {
	return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
}

// approve requires interactive affirmative consent and rejects broad --yes bypasses.
func (s *session) approve(question string) error {
	// Reject this path: --yes is not allowed; review and approve each deployment interactively.
	if s.flags["yes"] != "" {
		return model.Err("APPROVAL_REQUIRED", "--yes is not allowed; review and approve each deployment interactively")
	}
	// Noninteractive agents may propose work; the controller separately requires a human hardware signature.
	if !s.interactive {
		return nil
	}
	// A signed window avoids redundant prompting; proposal validation still enforces exact scope.
	if device, ok := s.remote.(interface{ HasDeploymentAuthorization(context.Context) bool }); ok && device.HasDeploymentAuthorization(s.ctx) {
		return nil
	}
	answer, err := s.ask(question, false)
	// Honor input cancellation or read failure before consuming the supplied value.
	if err != nil {
		return err
	}
	// Reject this path: Cancelled; no changes applied.
	if !affirmative(answer) {
		return model.Err("CANCELLED", "Cancelled; no changes applied")
	}
	return nil
}

// projectPath resolves the configured project file relative to the command working directory.
func (s *session) projectPath() string {
	file := s.flags["file"]
	// Use the standard project filename when --file was omitted.
	if file == "" {
		file = ".hostlier"
	}
	// Resolve relative project paths from the invocation working directory.
	if !filepath.IsAbs(file) {
		file = filepath.Join(s.cwd, file)
	}
	return file
}

// pause waits between polls while honoring context cancellation.
func (s *session) pause() error {
	interval := s.options.PollInterval
	// Use one-second polling unless tests or callers configured an interval.
	if interval == 0 {
		interval = time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	// Wait for the next polling interval or invocation cancellation.
	select {
	// Abort polling promptly when the invocation is cancelled.
	case <-s.ctx.Done():
		return s.ctx.Err()
	// Resume polling after the configured interval.
	case <-timer.C:
		return nil
	}
}

// defaultValue returns the first configured value, falling back to the supplied default.
func defaultValue(value, fallback string) string {
	// Preserve explicitly supplied values.
	if value != "" {
		return value
	}
	return fallback
}

// showHelp handles the three equivalent help entry points.
func (s *session) showHelp() bool {
	// Continue ordinary command execution when no help was requested; topic guides always answer with help.
	if s.command != "" && s.command != "help" && s.flags["help"] == "" && !bundled.IsTopic(s.command) {
		return false
	}
	// Resolve focused command help before showing the command index.
	if s.showCommandHelp() {
		return true
	}
	s.message(topLevelHelp())
	return true
}
