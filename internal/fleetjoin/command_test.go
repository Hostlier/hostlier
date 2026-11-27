package fleetjoin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

type requestFunc func(context.Context, string, string, any, any) error

func (f requestFunc) Request(ctx context.Context, method, path string, body, out any) error {
	return f(ctx, method, path, body, out)
}
func setResult(value, result any) error {
	if result == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

// TestConnectRestoresSelection ensures failure after approval cannot strand the member on a new master.
func TestConnectRestoresSelection(t *testing.T) {
	identity, err := fleet.Generate()
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	request, err := fleet.SignJoin(identity, fleet.JoinRequest{MasterURL: "https://master.example.com", Name: "worker", Hostname: "worker.example.com", TokenHash: fleet.JoinTokenHash(token), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := fleet.Fingerprint(identity.Certificate)
	prepared := fleet.PreparedJoin{Request: request, Token: token, Previous: strings.Repeat("b", 64), PreviousURL: "https://old.example.com:7464"}
	review := fleet.JoinReview{Code: fleet.JoinCode(request), Name: request.Name, Fingerprint: pin, MasterFingerprint: strings.Repeat("c", 64), MasterURL: "https://master.example.com:7464", ExpiresAt: request.ExpiresAt, Approved: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var changes []fleet.MasterChange
	local := requestFunc(func(callCtx context.Context, method, path string, body, out any) error {
		if path == "/v1/fleet-joins/prepare" {
			return setResult(prepared, out)
		}
		if path != "/v1/fleet/master" || method != "PUT" {
			t.Fatal(method, path)
		}
		changes = append(changes, body.(fleet.MasterChange))
		if len(changes) == 2 && callCtx.Err() != nil {
			t.Fatal("restore inherited cancelled context")
		}
		return nil
	})
	polls := 0
	remote := requestFunc(func(_ context.Context, method, path string, body, out any) error {
		if path == "/v1/fleet-joins/poll" {
			polls++
			if polls == 2 {
				cancel()
				return model.Err("HOST_REVOKED", "join revoked")
			}
		}
		return setResult(review, out)
	})
	var output bytes.Buffer
	err = Connect(ctx, local, remote, request.MasterURL, "worker", &output)
	if err == nil || !strings.Contains(err.Error(), "previous master restored") || len(changes) != 2 {
		t.Fatal(changes, err)
	}
	if changes[0].Previous != prepared.Previous || *changes[0].PreviousURL != prepared.PreviousURL || changes[1].Previous != review.MasterFingerprint || *changes[1].PreviousURL != review.MasterURL || changes[1].Fingerprint != prepared.Previous || changes[1].URL != prepared.PreviousURL {
		t.Fatal("unbound master switch or restore", changes)
	}
	if strings.Contains(output.String(), token) {
		t.Fatal("polling token printed")
	}
}

// TestApproveRequiresConfirmation ensures an omitted or negative response never mutates enrollment.
func TestApproveRequiresConfirmation(t *testing.T) {
	for _, answer := range []string{"", "no\n", "yes\n"} {
		writes := 0
		remote := requestFunc(func(_ context.Context, method, path string, body, out any) error {
			if method == "POST" {
				writes++
				return nil
			}
			return setResult(fleet.JoinReview{Code: "01234567-89ABCDEF", Name: "worker", Fingerprint: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, out)
		})
		err := Approve(context.Background(), remote, "01234567-89ABCDEF", strings.NewReader(answer), io.Discard)
		if (err == nil) != (answer == "yes\n") || writes != map[bool]int{true: 1, false: 0}[answer == "yes\n"] {
			t.Fatal(answer, writes, err)
		}
	}
}

// TestStandaloneApproveRejectsAutomation ensures --yes and piped input cannot confirm a controller.
func TestStandaloneApproveRejectsAutomation(t *testing.T) {
	for _, args := range [][]string{{"01234567-89ABCDEF"}, {"01234567-89ABCDEF", "--yes"}} {
		err := RunApprove(context.Background(), args, strings.NewReader("yes\n"), io.Discard, io.Discard)
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("noninteractive approval admitted", err)
		}
	}
}
