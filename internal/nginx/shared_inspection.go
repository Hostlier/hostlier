package nginx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// limitedDump avoids retaining unlimited configuration or command diagnostics in memory.
type limitedDump struct{ bytes.Buffer }

// Write stops inspection if a configuration dump exceeds the supported bound.
func (b *limitedDump) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 { // Bound configuration inspection memory.
		return 0, errors.New("Nginx configuration exceeds 4 MiB inspection limit")
	}
	return b.Buffer.Write(p)
}

// dumpConfiguration reads Nginx's effective on-disk include tree without exposing its contents to logs.
func dumpConfiguration(ctx context.Context, main string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nginx", "-T", "-c", main)
	var out limitedDump
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil { // Do not use incomplete or invalid Nginx inspection results.
		return nil, errors.New("cannot inspect existing Nginx configuration; run nginx -t locally")
	}
	return out.Bytes(), nil
}

// dumpedFiles separates Nginx's file headings, retaining only the configuration below each heading.
func dumpedFiles(dump []byte) (map[string]string, error) {
	files := map[string]string{}
	path := ""
	for _, line := range strings.Split(string(dump), "\n") { // Retain each effective configuration file within its own inspection boundary.
		if strings.HasPrefix(line, "# configuration file ") && strings.HasSuffix(line, ":") { // Retain each effective configuration file within its own inspection boundary.
			path = strings.TrimSuffix(strings.TrimPrefix(line, "# configuration file "), ":")
			if !filepath.IsAbs(path) { // Retain each effective configuration file within its own inspection boundary.
				return nil, errors.New("Nginx configuration dump contains a relative filename")
			}
			continue
		}
		if path != "" { // Retain each effective configuration file within its own inspection boundary.
			files[path] += line + "\n"
		}
	}
	if len(files) == 0 { // Retain each effective configuration file within its own inspection boundary.
		return nil, errors.New("empty Nginx configuration dump")
	}
	return files, nil
}

// nginxTokens recognizes comments, quotes and escaped characters before inspecting directives.
func nginxTokens(text string) ([]string, error) {
	lexer := nginxLexer{}
	for _, c := range text { // Separate lexical content from directive structure before inspection.
		if !lexer.quoted(c) { // Separate lexical content from directive structure before inspection.
			lexer.consume(c)
		}
	}
	lexer.flush()
	if lexer.quote != 0 || lexer.escaped { // Separate lexical content from directive structure before inspection.
		return nil, errors.New("unsupported Nginx quoting")
	}
	return lexer.tokens, nil
}

// nginxLexer tracks quoted words separately from structural directive separators.
type nginxLexer struct {
	tokens           []string
	word             strings.Builder
	quote            rune
	escaped, comment bool
}

// flush publishes one completed word without including syntactic quote delimiters.
func (l *nginxLexer) flush() {
	if l.word.Len() > 0 { // Emit a completed configuration word only when one is present.
		l.tokens = append(l.tokens, l.word.String())
		l.word.Reset()
	}
}

// quoted consumes comments and escaped/quoted content before interpreting directive punctuation.
func (l *nginxLexer) quoted(c rune) bool {
	if l.comment { // Keep comments, escapes and quoted values out of structural parsing.
		if c == '\n' { // Keep comments, escapes and quoted values out of structural parsing.
			l.comment = false
		}
		return true
	}
	if l.escaped { // Keep comments, escapes and quoted values out of structural parsing.
		l.word.WriteRune('\\')
		l.word.WriteRune(c)
		l.escaped = false
		return true
	}
	if c == '\\' { // Keep comments, escapes and quoted values out of structural parsing.
		l.escaped = true
		return true
	}
	if l.quote != 0 { // Keep comments, escapes and quoted values out of structural parsing.
		if c == l.quote { // Keep comments, escapes and quoted values out of structural parsing.
			l.quote = 0
		} else {
			l.word.WriteRune(c)
		}
		return true
	}
	return false
}

// consume records unquoted directive structure and starts quoted/comment regions.
func (l *nginxLexer) consume(c rune) {
	switch c { // Interpret punctuation only outside quoted and escaped content.
	case '\'', '"': // Interpret punctuation only outside quoted and escaped content.
		l.quote = c
	case '#': // Interpret punctuation only outside quoted and escaped content.
		l.flush()
		l.comment = true
	case ' ', '\t', '\r', '\n': // Interpret punctuation only outside quoted and escaped content.
		l.flush()
	case ';', '{', '}': // Interpret punctuation only outside quoted and escaped content.
		l.flush()
		l.tokens = append(l.tokens, string(c))
	default: // Interpret punctuation only outside quoted and escaped content.
		l.word.WriteRune(c)
	}
}

// inspectSharedDump requires an existing HTTP include and rejects any competing hostname or default change.
func inspectSharedDump(o SharedOptions, dump []byte, hosts []string) error {
	files, err := dumpedFiles(dump)
	if err != nil { // Require a safe existing include layout before adding virtual hosts.
		return err
	}
	main, exists := files[o.MainConfig]
	if !exists { // Require a safe existing include layout before adding virtual hosts.
		return errors.New("existing Nginx main configuration was not inspected")
	}
	tokens, err := nginxTokens(main)
	if err != nil { // Require a safe existing include layout before adding virtual hosts.
		return err
	}
	if !hasHTTPInclude(tokens, o.Fragment) { // Require a safe existing include layout before adding virtual hosts.
		return fmt.Errorf("existing http block must already include %s/*.conf; Hostlier will not edit nginx.conf", filepath.Dir(o.Fragment))
	}
	defaults := map[string]bool{}
	listeners := map[string]bool{}
	if err = inspectForeignFiles(o, files, hosts, listeners, defaults); err != nil { // Require a safe existing include layout before adding virtual hosts.
		return err
	}
	// New includes must never become the default virtual host for pre-existing listeners.
	for _, port := range []string{o.HTTPAddress, o.HTTPSAddress} {
		if listeners[port] && !defaults[port] { // Require a safe existing include layout before adding virtual hosts.
			return fmt.Errorf("existing listen %s needs an explicit default_server before coexistence; Hostlier will not alter existing listeners", port)
		}
	}
	return nil
}

// inspectForeignFiles scans every effective file except the separately verified owned include.
func inspectForeignFiles(o SharedOptions, files map[string]string, hosts []string, listeners, defaults map[string]bool) error {
	for path, text := range files { // Preserve external hostname reservations across the entire include tree.
		if path == o.Fragment { // Preserve external hostname reservations across the entire include tree.
			continue
		}
		tokens, err := nginxTokens(text)
		if err != nil { // Preserve external hostname reservations across the entire include tree.
			return err
		}
		if err = inspectDirectives(tokens, hosts, listeners, defaults); err != nil { // Preserve external hostname reservations across the entire include tree.
			return err
		}
	}
	return nil
}

// hasHTTPInclude accepts the standard top-level http include without changing arbitrary configuration trees.
func hasHTTPInclude(tokens []string, fragment string) bool {
	var stack []string
	var directive []string
	for _, token := range tokens { // Track block context so only a direct HTTP include authorizes publication.
		switch token { // Track block context so only a direct HTTP include authorizes publication.
		case "{": // Track block context so only a direct HTTP include authorizes publication.
			name := ""
			if len(directive) > 0 { // Track block context so only a direct HTTP include authorizes publication.
				name = directive[0]
			}
			stack = append(stack, name)
			directive = nil
		case "}": // Track block context so only a direct HTTP include authorizes publication.
			if len(stack) > 0 { // Track block context so only a direct HTTP include authorizes publication.
				stack = stack[:len(stack)-1]
			}
			directive = nil
		case ";": // Track block context so only a direct HTTP include authorizes publication.
			if isHTTPInclude(stack, directive, fragment) { // Track block context so only a direct HTTP include authorizes publication.
				return true
			}
			directive = nil
		default: // Track block context so only a direct HTTP include authorizes publication.
			directive = append(directive, token)
		}
	}
	return false
}

// isHTTPInclude restricts publication to the standard wildcard directly within the http block.
func isHTTPInclude(stack, directive []string, fragment string) bool {
	return len(stack) == 1 && stack[0] == "http" && len(directive) == 2 && directive[0] == "include" && directive[1] == filepath.Dir(fragment)+"/*.conf"
}

// inspectDirectives treats regex/dynamic hostnames conservatively rather than risking traffic takeover.
func inspectDirectives(tokens, hosts []string, listeners, defaults map[string]bool) error {
	var directive []string
	for _, token := range tokens { // Inspect complete directives without interpreting comments as configuration.
		if strings.Contains(token, "$hostlier_connection_upgrade") { // Inspect complete directives without interpreting comments as configuration.
			return errors.New("existing Nginx configuration uses Hostlier's reserved upgrade variable")
		}
		switch token { // Inspect complete directives without interpreting comments as configuration.
		case "{", "}": // Inspect complete directives without interpreting comments as configuration.
			directive = nil
		case ";": // Inspect complete directives without interpreting comments as configuration.
			if err := inspectDirective(directive, hosts, listeners, defaults); err != nil { // Inspect complete directives without interpreting comments as configuration.
				return err
			}
			directive = nil
		default: // Inspect complete directives without interpreting comments as configuration.
			directive = append(directive, token)
		}
	}
	return nil
}

// inspectDirective checks explicit and wildcard hostname reservations and wildcard IPv4 defaults.
func inspectDirective(directive, hosts []string, listeners, defaults map[string]bool) error {
	if len(directive) < 2 { // Record listener defaults separately from hostname reservations.
		return nil
	}
	switch directive[0] { // Record listener defaults separately from hostname reservations.
	case "listen": // Record listener defaults separately from hostname reservations.
		port := strings.TrimPrefix(strings.TrimPrefix(directive[1], "0.0.0.0:"), "*:")
		listeners[port] = true
		for _, option := range directive[2:] { // Record listener defaults separately from hostname reservations.
			if option == "default_server" || option == "default" { // Record listener defaults separately from hostname reservations.
				defaults[port] = true
			}
		}
	case "server_name": // Record listener defaults separately from hostname reservations.
		return inspectServerNames(directive[1:], hosts)
	}
	return nil
}

// inspectServerNames refuses names that could already own a requested hostname.
func inspectServerNames(names, hosts []string) error {
	for _, name := range names { // Reject names whose existing ownership may overlap the requested hostname.
		if strings.HasPrefix(name, "~") { // A fixed DNS tail proves disjoint regex ownership without interpreting PCRE.
			suffix, ok := regexServerNameSuffix(name)
			if !ok {
				return fmt.Errorf("Nginx server_name regex %q requires a separately reviewed integration", name)
			}
			for _, host := range hosts {
				if strings.HasSuffix(strings.ToLower(host), suffix) {
					return fmt.Errorf("hostname %s may already be served by existing Nginx regex %q", host, name)
				}
			}
			continue
		}
		if strings.Contains(name, "$") {
			return errors.New("dynamic Nginx server_name requires a separately reviewed integration")
		}
		for _, host := range hosts { // Reject names whose existing ownership may overlap the requested hostname.
			if hostnameMatches(name, host) { // Reject names whose existing ownership may overlap the requested hostname.
				return fmt.Errorf("hostname %s is already served by existing Nginx configuration", host)
			}
		}
	}
	return nil
}

// regexServerNameSuffix accepts only anchored regexes with a trailing literal DNS suffix.
func regexServerNameSuffix(name string) (string, bool) {
	pattern := strings.TrimPrefix(strings.TrimPrefix(name, "~*"), "~")
	if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") || strings.ContainsAny(pattern[:len(pattern)-1], "|$") {
		return "", false
	}
	pattern = pattern[:len(pattern)-1]
	i := len(pattern) - 1
	for i >= 0 {
		c := pattern[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			i--
			continue
		}
		if c == '.' && i > 0 && pattern[i-1] == '\\' && (i < 2 || pattern[i-2] != '\\') {
			i -= 2
			continue
		}
		break
	}
	suffix := strings.ToLower(strings.ReplaceAll(pattern[i+1:], `\.`, "."))
	return suffix, strings.Count(suffix, ".") >= 2
}

// hostnameMatches includes Nginx's leading-dot and leading/trailing wildcard names.
func hostnameMatches(name, host string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == host { // Apply Nginx exact and wildcard hostname matching rules.
		return true
	}
	if strings.HasPrefix(name, "*.") { // Apply Nginx exact and wildcard hostname matching rules.
		return strings.HasSuffix(host, name[1:])
	}
	if strings.HasPrefix(name, ".") { // Apply Nginx exact and wildcard hostname matching rules.
		return host == name[1:] || strings.HasSuffix(host, name)
	}
	return strings.HasSuffix(name, ".*") && strings.HasPrefix(host, strings.TrimSuffix(name, "*"))
}
