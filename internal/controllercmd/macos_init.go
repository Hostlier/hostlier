package controllercmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/controller"
	"hostlier/internal/model"
)

const macControllerLabel = "com.hostlier.controller"

var macDaemonFlags = []string{"config", "controller-hostname", "host", "port", "fleet-address", "socket", "tls-cert", "tls-key", "auth-config"}

type macInitSystem struct {
	home, executable string
	uid              int
	launch           func(context.Context, ...string) (string, error)
	ready            func(context.Context, macInitPlan) error
	confirm          func() (bool, error)
}

type macInitPlan struct {
	Supported          bool              `json:"supported"`
	Runtime            string            `json:"runtime"`
	Domain             string            `json:"domain"`
	ControllerHostname string            `json:"controllerHostname"`
	APIAddress         string            `json:"apiAddress"`
	StateDir           string            `json:"stateDir"`
	ConfigPath         string            `json:"configPath"`
	SocketPath         string            `json:"socketPath"`
	Executable         string            `json:"executable"`
	Label              string            `json:"label"`
	LaunchAgent        string            `json:"launchAgent"`
	Changes            []string          `json:"changes"`
	Fingerprint        string            `json:"fingerprint"`
	DaemonOptions      map[string]string `json:"daemonOptions"`
	SourceSHA256       string            `json:"sourceSHA256"`
	ConfigSHA256       string            `json:"configSHA256,omitempty"`
	PriorAgentSHA256   string            `json:"priorAgentSHA256,omitempty"`
	AlreadyLoaded      bool              `json:"alreadyLoaded"`

	config, plist, previous []byte
	previousBinary          string
}

func runMacInit(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, sys macInitSystem) error {
	flags := flag.NewFlagSet("hostlier-controller init", flag.ContinueOnError)
	flags.SetOutput(errOut)
	domain := flags.String("domain", "", "application domain for a new control plane")
	state := flags.String("state-dir", "", "controller state directory")
	runtime := flags.String("runtime", "", "auto or control-plane")
	dry := flags.Bool("dry-run", false, "preview without changing this Mac")
	yes := flags.Bool("yes", false, "approve the displayed installation")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	fingerprint := flags.String("approved-fingerprint", "", "require this exact reviewed installation")
	values := map[string]*string{}
	for _, name := range macDaemonFlags {
		values[name] = flags.String(name, "", "control-plane "+name)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return model.Err("ARGUMENTS", "macOS init accepts flags only")
	}
	if *runtime != "" && *runtime != "auto" && *runtime != controller.ControlPlaneMode {
		return model.Err("INSTALL_RUNTIME", "macOS init installs the Hostlier control plane")
	}
	if err := sys.resolve(); err != nil {
		return err
	}
	settings := map[string]string{}
	flags.Visit(func(f *flag.Flag) {
		if value := values[f.Name]; value != nil {
			settings[f.Name] = *value
		}
	})
	inspect := func() (macInitPlan, error) { return sys.inspect(ctx, *state, *domain, settings) }
	plan, err := inspect()
	if err != nil {
		return err
	}
	if *fingerprint != "" && plan.Fingerprint != *fingerprint {
		return model.Err("STALE_PLAN", "Review the updated macOS installation plan")
	}
	if err := writeMacInitPlan(out, plan, *jsonOutput); err != nil || *dry {
		return err
	}
	if err := approveInstallation(*yes, in, errOut, sys.confirm); err != nil {
		return err
	}
	if err := sys.apply(ctx, plan, inspect); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(map[string]bool{"installed": true})
	}
	_, err = fmt.Fprintf(out, "\nHostlier control plane is running with launchd.\nAPI: %s\nAdmin socket: %s\n", plan.APIAddress, plan.SocketPath)
	return err
}

func (sys *macInitSystem) resolve() error {
	if sys.home == "" {
		var err error
		sys.home, err = os.UserHomeDir()
		if err != nil {
			return err
		}
		sys.uid = os.Geteuid()
	}
	if sys.uid == 0 {
		return model.Err("INSTALL_PLATFORM", "Run macOS init as your logged-in user without sudo")
	}
	home, err := filepath.EvalSymlinks(sys.home)
	if err != nil {
		return err
	}
	sys.home = home
	executable, err := initExecutable(sys.executable)
	if err != nil {
		return err
	}
	sys.executable = executable
	if sys.launch == nil {
		sys.launch = func(ctx context.Context, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
			return string(out), err
		}
	}
	if sys.ready == nil {
		sys.ready = waitMacControlPlane
	}
	return nil
}

func (sys macInitSystem) domain() string { return "gui/" + strconv.Itoa(sys.uid) }

func (sys macInitSystem) inspect(ctx context.Context, dir, domain string, overrides map[string]string) (macInitPlan, error) {
	homeState := filepath.Join(sys.home, ".hostlier", "control-plane")
	state, err := filepath.Abs(defaultValue(dir, homeState))
	if err != nil {
		return macInitPlan{}, err
	}
	label := macControllerLabel
	if state != homeState {
		label += "." + macDigest([]byte(state))[:16]
	}
	p := macInitPlan{Supported: true, Runtime: controller.ControlPlaneMode, StateDir: state, Label: label,
		LaunchAgent:   filepath.Join(sys.home, "Library", "LaunchAgents", label+".plist"),
		DaemonOptions: map[string]string{}}
	if err := p.readPriorAgent(); err != nil {
		return p, err
	}
	for name, value := range overrides {
		p.DaemonOptions[name] = value
	}
	if err := p.resolveConfiguration(domain); err != nil {
		return p, err
	}
	if err := p.prepareAgent(sys.executable); err != nil {
		return p, err
	}
	if err := sys.inspectLaunchd(ctx, &p); err != nil {
		return p, err
	}
	p.Changes = []string{"Install the verified controller at " + p.Executable, "Start " + p.Label + " with launchd now and at login"}
	if p.config != nil {
		p.Changes = append(p.Changes, "Create "+p.ConfigPath)
	} else {
		p.Changes = append(p.Changes, "Use the existing configuration at "+p.ConfigPath)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	p.Fingerprint = macDigest(data)
	return p, nil
}

func (p *macInitPlan) resolveConfiguration(domain string) error {
	options := p.DaemonOptions
	options["config"] = defaultValue(options["config"], filepath.Join(p.StateDir, "config.yaml"))
	options["socket"] = defaultValue(options["socket"], filepath.Join(p.StateDir, "controller.sock"))
	for _, name := range []string{"config", "socket", "tls-cert", "tls-key", "auth-config"} {
		if options[name] != "" {
			path, err := filepath.Abs(options[name])
			if err != nil {
				return err
			}
			options[name] = path
		}
	}
	p.ConfigPath, p.SocketPath = options["config"], options["socket"]
	if len(p.SocketPath) >= 104 {
		return model.Err("INSTALL_PATH", "Choose a shorter --socket path for the macOS Unix socket")
	}
	data, err := readMacInstallFile(p.ConfigPath)
	if err != nil {
		return err
	}
	if data == nil {
		p.Domain, err = config.NormalizeDomain(domain)
		if err != nil {
			return model.Err("INSTALL_DOMAIN", "Supply --domain to create the macOS control-plane configuration")
		}
		p.config = []byte("version: 1\ndomains:\n  - " + p.Domain + "\n")
	} else {
		cfg, err := config.ReadServer(p.ConfigPath)
		if err != nil {
			return err
		}
		p.Domain = cfg.Domains[0]
		if domain != "" && !strings.EqualFold(domain, p.Domain) {
			return model.Err("INSTALL_DOMAIN", "The requested domain differs from the existing control-plane configuration")
		}
		p.ConfigSHA256 = macDigest(data)
	}
	p.ControllerHostname, err = config.NormalizeDomain(defaultValue(options["controller-hostname"], "hostlier."+p.Domain))
	if err != nil {
		return err
	}
	options["controller-hostname"] = p.ControllerHostname
	s := session{flags: options}
	listeners, err := s.controlPlaneListeners()
	if err != nil {
		return err
	}
	if options["auth-config"] != "" {
		if _, err := s.controllerEmail(p.ControllerHostname); err != nil {
			return err
		}
	}
	p.APIAddress = "http://" + net.JoinHostPort(listeners.Host, strconv.Itoa(listeners.Port))
	if listeners.TLSConfig != nil {
		p.APIAddress = "https://" + net.JoinHostPort(p.ControllerHostname, strconv.Itoa(listeners.Port))
	}
	return nil
}

func writeMacInitPlan(out io.Writer, p macInitPlan, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(out).Encode(p)
	}
	if _, err := fmt.Fprintf(out, "Hostlier macOS control-plane installation\nAPI: %s\nState: %s\nLaunchAgent: %s\n", p.APIAddress, p.StateDir, p.LaunchAgent); err != nil {
		return err
	}
	for _, change := range p.Changes {
		if _, err := fmt.Fprintln(out, "  - "+change); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Plan fingerprint:", p.Fingerprint)
	return err
}
