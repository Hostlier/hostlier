package nginx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// UpgradeSharedController refreshes only the generated controller TLS server.
// Other Hostlier routes and the operator's Nginx files remain byte-for-byte intact.
func UpgradeSharedController(ctx context.Context, options SharedOptions, controller, upstream string) error {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	o := SharedDefaults(options)
	unlock, err := lockShared(ctx, o)
	if err != nil {
		return err
	}
	defer unlock()
	// Require the prior generated include, its ownership record and live certificate.
	if err := CheckSharedController(ctx, o, controller); err != nil {
		return err
	}
	previous, err := sharedPrevious(o)
	if err != nil {
		return err
	}
	start, end, err := sharedControllerServerSpan(previous, o.HTTPSAddress, controller)
	if err != nil {
		return err
	}
	certificates, err := sharedCertificates(o, []string{controller})
	if err != nil {
		return err
	}
	server, err := hostServer(o.Options, controller, controller, upstream, nil)
	if err != nil {
		return err
	}
	owned := filepath.Join(o.CertificateDirectory, "live", controller)
	server = strings.ReplaceAll(server, owned+"/fullchain.pem", certificates[controller].Certificate)
	server = strings.ReplaceAll(server, owned+"/privkey.pem", certificates[controller].Key)
	server = strings.ReplaceAll(server, "$connection_upgrade", "$hostlier_connection_upgrade")
	server = strings.TrimSuffix(strings.TrimPrefix(server, " "), "\n")
	candidate := make([]byte, 0, len(previous)-(end-start)+len(server))
	candidate = append(candidate, previous[:start]...)
	candidate = append(candidate, server...)
	candidate = append(candidate, previous[end:]...)
	if bytes.Equal(previous, candidate) {
		return nil
	}
	// The shared publisher validates the complete Nginx configuration, reloads
	// gracefully and restores the old include if validation or reload fails.
	return publishShared(ctx, o, candidate, []string{controller})
}

// sharedControllerServerSpan finds exactly one previously generated TLS server.
func sharedControllerServerSpan(data []byte, listener, controller string) (int, int, error) {
	text := string(data)
	start := -1
	for _, marker := range []string{
		fmt.Sprintf("server { listen %s ssl http2; server_name %s;", listener, controller),
		fmt.Sprintf("server { listen %s ssl; server_name %s;", listener, controller),
	} {
		if count := strings.Count(text, marker); count > 1 {
			return 0, 0, errors.New("multiple Hostlier controller TLS routes in owned Nginx include")
		} else if count == 1 {
			if start >= 0 {
				return 0, 0, errors.New("multiple Hostlier controller TLS routes in owned Nginx include")
			}
			start = strings.Index(text, marker)
		}
	}
	if start < 0 {
		return 0, 0, errors.New("Hostlier controller TLS route is incomplete; inspect the owned include before retrying initialization")
	}
	end, err := nginxServerBlockEnd(data, start)
	return start, end, err
}

// nginxServerBlockEnd ignores quoted regex quantifiers and comments while
// locating the matching close of a generated server block.
func nginxServerBlockEnd(data []byte, start int) (int, error) {
	depth := 0
	quote := byte(0)
	escaped, comment, opened := false, false, false
	for i := start; i < len(data); i++ {
		c := data[i]
		if comment {
			if c == '\n' {
				comment = false
			}
			continue
		}
		if quote != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '#':
			comment = true
		case '\'', '"':
			quote = c
		case '{':
			opened = true
			depth++
		case '}':
			depth--
			if !opened || depth < 0 {
				return 0, errors.New("invalid Hostlier controller TLS route")
			}
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, errors.New("unterminated Hostlier controller TLS route")
}
