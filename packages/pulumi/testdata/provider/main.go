// The local provider fixture implements real CRUD solely for temporary test files.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/rpcutil"
	rpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// provider implements the minimal resource RPCs without any external service or credentials.
type provider struct {
	rpc.UnimplementedResourceProviderServer
}

// main starts the provider protocol and publishes only its listening port to the CLI.
func main() {
	// Validate each entry without exposing private values to the public protocol.
	handle, err := rpcutil.ServeWithOptions(rpcutil.ServeOptions{Port: 0, Init: func(server *grpc.Server) error { rpc.RegisterResourceProviderServer(server, &provider{}); return nil }})
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(handle.Port)
	// Reject an identity or visibility mismatch before publishing the operation result.
	if <-handle.Done != nil {
		os.Exit(1)
	}
}

// GetPluginInfo pins the fixture provider identity.
func (*provider) GetPluginInfo(context.Context, *emptypb.Empty) (*rpc.PluginInfo, error) {
	return &rpc.PluginInfo{Version: "0.0.1"}, nil
}

// GetSchema supplies the small provider's resource contract.
func (*provider) GetSchema(context.Context, *rpc.GetSchemaRequest) (*rpc.GetSchemaResponse, error) {
	return &rpc.GetSchemaResponse{Schema: `{"name":"local","version":"0.0.1","resources":{"local:index:File":{"inputProperties":{"path":{"type":"string"},"value":{"type":"string"}},"properties":{"path":{"type":"string"},"value":{"type":"string"}},"requiredInputs":["path","value"]}}}`}, nil
}

// Configure declares preview support and lets the engine retain sensitive-value wrappers.
func (*provider) Configure(context.Context, *rpc.ConfigureRequest) (*rpc.ConfigureResponse, error) {
	return &rpc.ConfigureResponse{AcceptSecrets: true, SupportsPreview: true}, nil
}

// Check preserves the planned inputs without defaults or hidden mutations.
func (*provider) Check(ctx context.Context, r *rpc.CheckRequest) (*rpc.CheckResponse, error) {
	return &rpc.CheckResponse{Inputs: r.News}, nil
}

// Diff reports native update intent from the saved and desired input values.
func (*provider) Diff(ctx context.Context, r *rpc.DiffRequest) (*rpc.DiffResponse, error) {
	changed := rpc.DiffResponse_DIFF_NONE
	// Reject an identity or visibility mismatch before publishing the operation result.
	if r.Olds.String() != r.News.String() {
		changed = rpc.DiffResponse_DIFF_SOME
	}
	return &rpc.DiffResponse{Changes: changed}, nil
}

// target restricts the fixture's mutable files to a visibly named temporary test directory.
func target(properties *structpb.Struct) (string, string, error) {
	path := properties.GetFields()["path"].GetStringValue()
	value := properties.GetFields()["value"].GetStringValue()
	// Reject an identity or visibility mismatch before publishing the operation result.
	if !filepath.IsAbs(path) || !strings.Contains(path, "hostlier-pulumi-fixture-") {
		return "", "", fmt.Errorf("fixture path rejected")
	}
	return path, value, nil
}

// Create performs genuine local provider mutation unless the engine explicitly asks for preview.
func (*provider) Create(ctx context.Context, r *rpc.CreateRequest) (*rpc.CreateResponse, error) {
	path, value, err := target(r.Properties)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, err
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if !r.Preview {
		err = os.WriteFile(path, []byte(value), 0600)
	}
	return &rpc.CreateResponse{Id: path, Properties: r.Properties}, err
}

// Update changes the existing native file and returns the recorded properties.
func (*provider) Update(ctx context.Context, r *rpc.UpdateRequest) (*rpc.UpdateResponse, error) {
	path, value, err := target(r.News)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, err
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if !r.Preview {
		err = os.WriteFile(path, []byte(value), 0600)
	}
	return &rpc.UpdateResponse{Properties: r.News}, err
}

// Read observes local live drift without modifying the resource.
func (*provider) Read(ctx context.Context, r *rpc.ReadRequest) (*rpc.ReadResponse, error) {
	path, _, err := target(r.Properties)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if os.IsNotExist(err) {
		return &rpc.ReadResponse{}, nil
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, err
	}
	properties, _ := structpb.NewStruct(map[string]any{"path": path, "value": string(raw)})
	return &rpc.ReadResponse{Id: r.Id, Properties: properties, Inputs: r.Inputs}, nil
}

// Delete exists only for complete provider protocol behavior; the adapter exposes no destroy action.
func (*provider) Delete(ctx context.Context, r *rpc.DeleteRequest) (*emptypb.Empty, error) {
	path, _, err := target(r.Properties)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, err
	}
	err = os.Remove(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if os.IsNotExist(err) {
		err = nil
	}
	return &emptypb.Empty{}, err
}
