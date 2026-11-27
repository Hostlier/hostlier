package fleetjoin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// Connect changes the local master only after approval of the member's signed identity.
func Connect(ctx context.Context, local, remote Requester, origin, name string, out io.Writer) error {
	var prepared fleet.PreparedJoin
	if err := local.Request(ctx, "POST", "/v1/fleet-joins/prepare", fleet.JoinPrepare{URL: origin, Name: name}, &prepared); err != nil {
		return err
	}
	if prepared.Request.MasterURL != origin || !fleet.ValidPin(prepared.Token) || fleet.JoinTokenHash(prepared.Token) != prepared.Request.TokenHash {
		return model.Err("JOIN_PROTOCOL", "Local controller returned invalid join proof")
	}
	deadline, cancel := context.WithDeadline(ctx, time.UnixMilli(prepared.Request.ExpiresAt))
	defer cancel()
	var review fleet.JoinReview
	if err := remote.Request(deadline, "POST", "/v1/fleet-joins/request", prepared.Request, &review); err != nil {
		return err
	}
	if err := validReview(review, prepared); err != nil {
		return err
	}
	if err := printPending(out, origin, review); err != nil {
		return err
	}
	review, err := waitForJoin(deadline, remote, prepared, false)
	if err != nil {
		return err
	}
	change := fleet.MasterChange{Previous: prepared.Previous, PreviousURL: &prepared.PreviousURL, Fingerprint: review.MasterFingerprint, URL: review.MasterURL}
	if err := local.Request(deadline, "PUT", "/v1/fleet/master", change, nil); err != nil {
		return fmt.Errorf("controller approved, saving master selection: %w", err)
	}
	connected, stop := context.WithTimeout(deadline, 45*time.Second)
	_, err = waitForJoin(connected, remote, prepared, true)
	stop()
	if err != nil {
		return restoreMaster(ctx, local, prepared, review, err)
	}
	_, err = fmt.Fprintf(out, "Controller %s connected to %s. Reconnection is automatic after service or server restarts.\n", review.Name, origin)
	return err
}

// waitForJoin retries safe observation only; it never repeats approval or execution.
func waitForJoin(ctx context.Context, remote Requester, prepared fleet.PreparedJoin, connected bool) (fleet.JoinReview, error) {
	poll := fleet.JoinPoll{Code: fleet.JoinCode(prepared.Request), Token: prepared.Token}
	for {
		var review fleet.JoinReview
		err := remote.Request(ctx, "POST", "/v1/fleet-joins/poll", poll, &review)
		if err == nil {
			if err := validReview(review, prepared); err != nil {
				return review, err
			}
			if review.Approved && (!connected || review.Connected) {
				return review, nil
			}
		} else {
			var failure *model.Error
			if !errors.As(err, &failure) || failure.Code != "CONNECTION" {
				return review, err
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return review, ctx.Err()
		case <-timer.C:
		}
	}
}

// restoreMaster cannot overwrite a concurrent administrator's pin or endpoint change.
func restoreMaster(ctx context.Context, local Requester, prepared fleet.PreparedJoin, review fleet.JoinReview, cause error) error {
	if prepared.Previous == review.MasterFingerprint && prepared.PreviousURL == review.MasterURL {
		return fmt.Errorf("controller enrolled; outbound connection was not verified: %w", cause)
	}
	restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	change := fleet.MasterChange{Previous: review.MasterFingerprint, PreviousURL: &review.MasterURL, Fingerprint: prepared.Previous, URL: prepared.PreviousURL}
	if err := local.Request(restore, "PUT", "/v1/fleet/master", change, nil); err != nil {
		return fmt.Errorf("outbound connection not verified: %w; restoring previous master: %v", cause, err)
	}
	return fmt.Errorf("outbound connection not verified; previous master restored: %w", cause)
}
