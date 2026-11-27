// Package fleetjoin implements controller-only enrollment without browser or user credentials.
package fleetjoin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// Requester lets both commands share the native gRPC client's bounded transport.
type Requester interface {
	Request(context.Context, string, string, any, any) error
}

// joinArguments parses flags before the command validates its target or opens a connection.
func joinArguments(flags *flag.FlagSet, args []string, usage string) (string, error) {
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), usage)
		flags.PrintDefaults()
	}
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if target == "" || flags.NArg() != 0 {
		return "", model.Err("ARGUMENTS", usage)
	}
	return target, nil
}

// RunConnect resolves only local administration and a verified public master origin.
func RunConnect(ctx context.Context, args []string, out, errOut io.Writer) error {
	const usage = "Usage: hostlier-controller connect HTTPS_MASTER [--name NAME] [--socket PATH] [--ca-file FILE]"
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(errOut)
	name := flags.String("name", "", "member controller name")
	socket := flags.String("socket", fleet.DefaultSocket, "local controller administration socket")
	ca := flags.String("ca-file", "", "trusted CA file for the master's public HTTPS endpoint")
	target, err := joinArguments(flags, args, usage)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	origin, err := model.ControllerURL(target)
	if err != nil {
		return err
	}
	local, err := newTransport("", *socket, "")
	if err != nil {
		return err
	}
	defer local.http.CloseIdleConnections()
	remote, err := newTransport(origin, "", *ca)
	if err != nil {
		return err
	}
	defer remote.http.CloseIdleConnections()
	return Connect(ctx, local, remote, origin, *name, out)
}

// RunApprove requires a real administrator terminal before accepting an approval.
func RunApprove(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	const usage = "Usage: hostlier-controller approve CODE [--socket PATH]"
	flags := flag.NewFlagSet("approve", flag.ContinueOnError)
	flags.SetOutput(errOut)
	socket := flags.String("socket", fleet.DefaultSocket, "local controller administration socket")
	code, err := joinArguments(flags, args, usage)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	terminal, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(terminal.Fd())) {
		return model.Err("INTERACTIVE_REQUIRED", "Controller approval requires an interactive administrator terminal")
	}
	local, err := newTransport("", *socket, "")
	if err != nil {
		return err
	}
	defer local.http.CloseIdleConnections()
	return Approve(ctx, local, strings.ToUpper(strings.TrimSpace(code)), in, out)
}

// validReview prevents a response from selecting a different member than the local proof.
func validReview(review fleet.JoinReview, prepared fleet.PreparedJoin) error {
	pin, err := fleet.Fingerprint(prepared.Request.Certificate)
	if err != nil {
		return err
	}
	if review.Code != fleet.JoinCode(prepared.Request) || review.Name != prepared.Request.Name || review.Fingerprint != pin || review.ExpiresAt != prepared.Request.ExpiresAt || !fleet.ValidPin(review.MasterFingerprint) || review.MasterFingerprint == pin || !fleet.ValidURL(review.MasterURL) {
		return model.Err("JOIN_PROTOCOL", "Master returned a different or invalid controller join identity")
	}
	return nil
}

// printPending presents only public review details, never the polling capability.
func printPending(out io.Writer, origin string, review fleet.JoinReview) error {
	_, err := fmt.Fprintf(out, "Waiting for approval on %s\nMember: %s\nApproval code: %s\nOn the master controller, run: sudo hostlier-controller approve %s\n", origin, review.Name, review.Code, review.Code)
	return err
}
