package githubpackages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// InstallSDK adds the pinned package-authoring dependency to an existing Go project.
func InstallSDK(ctx context.Context, directory, commit, token string) error {
	// Only the resolved canonical SDK repository may become this named dependency.
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit) {
		return fmt.Errorf("SDK requires a resolved Git commit")
	}
	info, err := os.Stat(filepath.Join(directory, "go.mod"))
	// Installing a library must not silently initialize or modify an unrelated project.
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("run hostlier sdk install from a Go project containing go.mod; initialize it with go mod init first")
	}
	environment, err := sdkEnvironment(os.Environ(), token)
	// Invalid inherited Git configuration cannot change credential-helper interpretation.
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "get", "github.com/"+DefaultRepository+"/sdk/packagekit@"+commit)
	command.Dir = directory
	command.Env = environment
	// Raw tool diagnostics can contain private repository URLs; return a bounded actionable failure.
	if err = command.Run(); err != nil {
		return fmt.Errorf("SDK dependency installation failed; verify Go, go.mod and GitHub access")
	}
	return nil
}

// sdkEnvironment keeps private-module and credential configuration scoped to this Go invocation.
func sdkEnvironment(environment []string, token string) ([]string, error) {
	private := environmentValue(environment, "GOPRIVATE")
	// Restrict private module discovery to the package repository while preserving existing selections.
	if private != "" {
		private += ","
	}
	environment = append(environment, "GOPRIVATE="+private+"github.com/"+DefaultRepository, "GIT_TERMINAL_PROMPT=0")
	return GitEnvironment(environment, token)
}

// GitEnvironment scopes GitHub credentials to a subprocess without writing global Git configuration.
func GitEnvironment(environment []string, token string) ([]string, error) {
	// Anonymous public installation does not need a credential helper.
	if token == "" {
		return environment, nil
	}
	count := 0
	// Existing command-scoped Git settings are preserved rather than replaced.
	if value := environmentValue(environment, "GIT_CONFIG_COUNT"); value != "" {
		var err error
		count, err = strconv.Atoi(value)
		// Bound inherited configuration before adding narrowly scoped credentials.
		if err != nil || count < 0 || count > 100 {
			return nil, fmt.Errorf("invalid inherited Git configuration")
		}
	}
	helper := `!f() { if [ "$1" = get ]; then printf 'username=x-access-token\npassword=%s\n' "$HOSTLIER_SDK_GITHUB_TOKEN"; fi; }; f`
	settings := []struct{ key, value string }{{"credential.https://github.com.helper", ""}, {"credential.https://github.com.helper", helper}}
	// The token travels only in the child environment, never in URLs, arguments or files.
	for i, setting := range settings {
		index := strconv.Itoa(count + i)
		environment = append(environment, "GIT_CONFIG_KEY_"+index+"="+setting.key, "GIT_CONFIG_VALUE_"+index+"="+setting.value)
	}
	return append(environment, "GIT_CONFIG_COUNT="+strconv.Itoa(count+len(settings)), "HOSTLIER_SDK_GITHUB_TOKEN="+token), nil
}

// environmentValue honors the same last-value-wins convention as an exec environment.
func environmentValue(environment []string, key string) string {
	value := ""
	// Inspect keys without exposing any inherited credential contents.
	for _, entry := range environment {
		// Values may contain equals signs, so only remove the exact key prefix.
		if strings.HasPrefix(entry, key+"=") {
			value = strings.TrimPrefix(entry, key+"=")
		}
	}
	return value
}
