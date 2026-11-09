package controllercmd

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"hostlier/internal/model"
)

// requestOwnerSetup sends one native gRPC command to the protected local
// socket without embedding the laptop CLI in the controller executable.
func requestOwnerSetup(ctx context.Context, socket string, input model.OwnerSetupRequest, result *model.OwnerSetupResult) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		Protocols: new(http.Protocols),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	transport.Protocols.SetUnencryptedHTTP2(true)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	api := controlrpcconnect.NewControllerClient(client, "http://localhost", connect.WithGRPC(), connect.WithReadMaxBytes(65536+1024))
	reply, err := api.Command(ctx, connect.NewRequest(&pb.CommandRequest{Method: "POST", Route: "/v1/access/setup", JsonBody: data}))
	if err != nil {
		var cause *connect.Error
		if errors.As(err, &cause) {
			if code := cause.Meta().Get("hostlier-error-code"); code != "" {
				return model.Err(code, cause.Message())
			}
		}
		return model.Err("CONTROLLER_UNAVAILABLE", "Cannot reach the controller administration socket; check the service version and socket permissions")
	}
	if len(reply.Msg.JsonBody) > 65536 || json.Unmarshal(reply.Msg.JsonBody, result) != nil {
		return model.Err("BOOTSTRAP_RESPONSE", "Invalid controller bootstrap response")
	}
	return nil
}
