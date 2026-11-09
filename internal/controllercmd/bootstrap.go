package controllercmd

import (
	"context"
	"hostlier/internal/model"
	"time"
)

// bootstrapOwner asks the running service for a first-owner link through its protected socket.
func (s *session) bootstrapOwner() error {
	allowed := map[string]bool{"url": true, "request": true, "name": true, "socket": true}
	// Daemon and recovery flags cannot silently influence owner enrollment.
	for flag, value := range s.flags {
		// Empty parser defaults carry no operator intent.
		if value != "" && !allowed[flag] {
			return model.Err("ARGUMENTS", "Unsupported bootstrap option --"+flag)
		}
	}
	address, err := model.ControllerURL(s.flags["url"])
	// Require an explicit origin and pending request to avoid anonymous first-owner enrollment.
	if err != nil || s.flags["request"] == "" {
		return model.Err("ARGUMENTS", "Usage: hostlier-controller bootstrap --url HTTPS_ORIGIN --request ID [--socket PATH --name OWNER]")
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	var result model.OwnerSetupResult
	input := model.OwnerSetupRequest{URL: address, RequestID: s.flags["request"], Name: defaultValue(s.flags["name"], "Owner")}
	// The daemon revalidates origin, pending device and existing owner state under its lock.
	if err = requestOwnerSetup(ctx, defaultValue(s.flags["socket"], "/run/hostlier/controller.sock"), input, &result); err != nil {
		return err
	}
	return s.print(result)
}
