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
		if bytes.Equal(previous, bootstrap) {
			rendered, err := Render(o, host, upstream, nil)
			if err != nil {
				return err
			}
			return Activate(ctx, o, rendered)
		}
		marker := strings.TrimSpace(gatewayHost(o, host))
		if strings.Count(string(previous), marker) != 1 {
			return fmt.Errorf("owned gateway controller route is ambiguous")
		}
		start = strings.Index(string(previous), marker)
		end, err = nginxServerBlockEnd(previous, start)
	} else {
		start, end, err = sharedControllerServerSpan(previous, o.HTTPSAddress, host)
	}
	if err != nil {
		return err
	}
	next := append([]byte{}, previous[:start]...)
	next = append(next, strings.TrimSpace(server)...)
	next = append(next, previous[end:]...)
	if bytes.Equal(previous, next) {
		return nil
	}
	return Activate(ctx, o, next)
}
