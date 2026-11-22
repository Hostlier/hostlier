package nginx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UpgradeController refreshes just the generated controller server, preserving
// every other virtual host, including existing application routes.
func UpgradeController(ctx context.Context, options Options, host, upstream string) error {
	o := defaults(options)
	previous, err := os.ReadFile(filepath.Join(o.Directory, "nginx.conf"))
	if err != nil {
		return err
	}
	server, err := hostServer(o, host, host, upstream, nil)
	if err != nil {
		return err
	}
	var start, end int
	if o.Gateway {
		bootstrap, _ := gatewayBootstrap(o)
		if bytes.Equal(upgradeGatewayHTTP2ServerHeaders(previous, o), bootstrap) {
			rendered, err := Render(o, host, upstream, nil)
			if err != nil {
				return err
			}
			return Activate(ctx, o, rendered)
		}
		start, end, err = gatewayControllerServerSpan(previous, o, host)
	} else {
		start, end, err = sharedControllerServerSpan(previous, o.HTTPSAddress, host)
	}
	if err != nil {
		return err
	}
	next := append([]byte{}, previous[:start]...)
	next = append(next, strings.TrimSpace(server)...)
	next = append(next, previous[end:]...)
	// Migrate any remaining generated server headers to "http2 on;" on Nginx 1.25.1+.
	if o.Gateway {
		next = upgradeGatewayHTTP2ServerHeaders(next, o)
	} else {
		next = upgradeHTTP2ServerHeaders(next, o.HTTPSAddress)
	}
	if bytes.Equal(previous, next) {
		return nil
	}
	return Activate(ctx, o, next)
}

// upgradeGatewayHTTP2ServerHeaders migrates generated gateway server headers to the 1.25.1+ http2 directive.
func upgradeGatewayHTTP2ServerHeaders(data []byte, o Options) []byte {
	// Keep the legacy listen parameter when the installed Nginx binary predates 1.25.1.
	if !supportsHTTP2Directive(nginxVersion()) {
		return data
	}
	oldHeader := fmt.Sprintf("server { listen %s; listen %s http2; server_name ", o.HTTPSAddress, o.H2Address)
	newHeader := fmt.Sprintf("server { listen %s; listen %s; http2 on; server_name ", o.HTTPSAddress, o.H2Address)
	return []byte(strings.ReplaceAll(string(data), oldHeader, newHeader))
}

// gatewayControllerServerSpan finds the generated gateway controller server block across Nginx HTTP/2 syntaxes.
func gatewayControllerServerSpan(data []byte, o Options, host string) (int, int, error) {
	text := string(data)
	start := -1
	// Accept both modern "http2 on;" and legacy "listen ... http2;" gateway controller headers.
	for _, marker := range []string{
		fmt.Sprintf("server { listen %s; listen %s; http2 on; server_name %s;", o.HTTPSAddress, o.H2Address, host),
		fmt.Sprintf("server { listen %s; listen %s http2; server_name %s;", o.HTTPSAddress, o.H2Address, host),
	} {
		count := strings.Count(text, marker)
		// Reject ambiguous configurations with duplicate controller server blocks.
		if count > 1 || count == 1 && start >= 0 {
			return 0, 0, fmt.Errorf("owned gateway controller route is ambiguous")
		}
		// Record the unique controller server block offset.
		if count == 1 {
			start = strings.Index(text, marker)
		}
	}
	// Require an existing controller server block when not upgrading from bootstrap.
	if start < 0 {
		return 0, 0, fmt.Errorf("owned gateway controller route is ambiguous")
	}
	end, err := nginxServerBlockEnd(data, start)
	return start, end, err
}
