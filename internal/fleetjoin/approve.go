package fleetjoin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// Approve displays immutable member identity and submits an explicit local decision.
func Approve(ctx context.Context, local Requester, code string, in io.Reader, out io.Writer) error {
	if len(code) != 17 {
		return model.Err("ARGUMENTS", "Supply the approval code printed on the joining controller")
	}
	path := "/v1/fleet-joins/" + url.PathEscape(code)
	var review fleet.JoinReview
	if err := local.Request(ctx, "GET", path, nil, &review); err != nil {
		return err
	}
	if review.Code != code || !fleet.ValidHostName(review.Name) || !fleet.ValidPin(review.Fingerprint) {
		return model.Err("JOIN_PROTOCOL", "Invalid controller join review")
	}
	if review.Approved {
		return model.Err("JOIN_APPROVED", "Controller join request was already approved")
	}
	if review.ExpiresAt <= time.Now().UnixMilli() {
		return model.Err("JOIN_EXPIRED", "Controller join request has expired")
	}
	if _, err := fmt.Fprintf(out, "Controller join request %s\nName: %q\nReported hostname: %q\nMember fingerprint: %s\nExpires: %s\nApprove this controller? [y/N] ", code, review.Name, review.Hostname, review.Fingerprint, time.UnixMilli(review.ExpiresAt).Format(time.RFC3339)); err != nil {
		return err
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return model.Err("CANCELLED", "Controller approval cancelled")
	}
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		return model.Err("CANCELLED", "Controller approval cancelled")
	}
	if err := local.Request(ctx, "POST", path+"/approve", fleet.JoinDecision{Fingerprint: review.Fingerprint}, nil); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Approved %s. The member controller will connect automatically.\n", review.Name)
	return err
}
