package engines

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"hostlier/internal/resources"
	"hostlier/pkg/workflow"
)

// TestDockerTLSWorkflows exercises actual native protocols and scope enforcement on disposable containers.
func TestDockerTLSWorkflows(t *testing.T) {
	// Container pulls and socket access are explicitly opt-in.
	if os.Getenv("HOSTLIER_TEST_DOCKER") != "1" {
		t.Skip("set HOSTLIER_TEST_DOCKER=1 for disposable Docker TLS fixtures")
	}
	for _, engine := range []string{"postgres", "redis"} { // Check each configured engine, namespace, or command independently.
		t.Run(engine, func(t *testing.T) { testDockerEngine(t, engine) }) // Keep each fixture invocation and cleanup bound to this test.
	}
}

// testDockerEngine applies an offline-reviewed package using a provider-style deferred connection and private CA.
func testDockerEngine(t *testing.T, engine string) {
	req, ctx := dockerRequest(t, engine)
	p := Package{Engine: engine}
	// The plan sees input references before provider credentials or CA exist.
	actual := req.Inputs
	req.Inputs = nil
	plan, err := p.Handle(ctx, req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	req.Inputs = actual
	req.Action = "apply"
	req.Prepared = plan.Prepared.Payload
	// Before apply, authoritative discovery proves both package-owned names are absent.
	assertInspection(t, ctx, p, req, workflow.NotStarted)
	response, err := p.Handle(ctx, req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	if response.Result.Outputs["caCertificatePEM"] != req.Inputs["caCertificatePEM"] { // Reject the operation: application trust was lost.
		t.Fatal("application trust was lost")
	}
	// Simulate losing apply's response: recover only through authenticated discovery, without Previous state.
	assertInspection(t, ctx, p, req, workflow.Completed)
	// A newly planned password has no ownership proof over the already-created role or ACL.
	wrong, err := p.plan(ctx, req, Config{})
	// Require independent approved credentials for the negative ownership test.
	if err != nil {
		t.Fatal(err)
	}
	foreign := req
	foreign.Prepared = wrong.Payload
	assertInspection(t, ctx, p, foreign, workflow.Uncertain)
	// Use returned credentials for real scoped commands and forbidden-operation checks.
	if engine == "postgres" {
		checkDockerPostgres(t, ctx, req, response.Result.Outputs)
		return
	}
	checkDockerRedis(t, ctx, req, response.Result.Outputs)
}

// checkDockerPostgres verifies owned schema writes and denies role administration.
func checkDockerPostgres(t *testing.T, ctx context.Context, req workflow.Request, outputs map[string]string) {
	app, err := connectPostgres(ctx, outputs["url"], req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	defer app.Close(ctx)
	if _, err = app.Exec(ctx, "CREATE TABLE scoped_test (id integer)"); err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	if _, err = app.Exec(ctx, "CREATE ROLE forbidden_test"); err == nil { // Reject the operation: application could create roles.
		t.Fatal("application could create roles")
	}
	// A certificate mismatch must fail instead of silently weakening verification.
	req.Inputs["caCertificatePEM"] = dockerCertificates(t).ca
	if other, err := connectPostgres(ctx, outputs["url"], req); err == nil { // Reject the operation: wrong certificate authority accepted.
		other.Close(ctx)
		t.Fatal("wrong certificate authority accepted")
	}
}

// checkDockerRedis verifies key patterns and dangerous command denial with real ACL authentication.
func checkDockerRedis(t *testing.T, ctx context.Context, req workflow.Request, outputs map[string]string) {
	options, err := redisOptions(outputs["url"], req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	app := redis.NewClient(options)
	defer app.Close()
	if err = app.Set(ctx, outputs["keyPrefix"]+"test", "ok", time.Minute).Err(); err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	if err = app.Set(ctx, "foreign:test", "bad", time.Minute).Err(); err == nil { // Reject the operation: cross-prefix write allowed.
		t.Fatal("cross-prefix write allowed")
	}
	for _, command := range []string{"FLUSHALL", "KEYS", "SELECT", "ACL"} { // Check each configured engine, namespace, or command independently.
		// A complete command is needed so syntax errors cannot masquerade as authorization enforcement.
		args := forbiddenRedisCommand(command)
		if err = app.Do(ctx, args...).Err(); err == nil || !strings.Contains(err.Error(), "NOPERM") { // Validate this boundary before advancing the engine workflow.
			t.Fatalf("%s not denied by ACL: %v", command, err)
		}
	}
}

// forbiddenRedisCommand supplies valid dangerous or cross-namespace requests.
func forbiddenRedisCommand(command string) []any {
	switch command { // Supply valid arguments for each forbidden Redis command.
	case "KEYS": // Handle this command or readiness event with its native arguments.
		return []any{command, "*"}
	case "SELECT": // Handle this command or readiness event with its native arguments.
		return []any{command, "1"}
	case "ACL": // Handle this command or readiness event with its native arguments.
		return []any{command, "LIST"}
	default: // Use the native command unchanged when no arguments are required.
		return []any{command}
	}
}

// dockerRequest creates a TLS fixture and waits for authenticated administrative readiness.
func dockerRequest(t *testing.T, engine string) (workflow.Request, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	certs := dockerCertificates(t)
	container, port := dockerContainer(t, ctx, engine, certs.directory)
	_ = container
	req := remoteRequest(engine)
	req.Key = "docker-" + engine + fmt.Sprint(time.Now().UnixNano())
	req.Secrets = nil
	req.Step.Inputs = map[string]string{"connection": "provider.connection", "caCertificatePEM": "provider.caCertificatePEM"}
	uri := "postgresql://hostlier:fixture-admin@127.0.0.1:" + port + "/postgres?sslmode=verify-full"
	if engine == "redis" { // Select the native engine protocol and its isolation rules.
		uri = "rediss://default:fixture-admin@127.0.0.1:" + port + "/0"
	}
	req.Inputs = map[string]string{"connection": uri, "caCertificatePEM": certs.ca}
	waitDockerEngine(t, ctx, engine, req)
	return req, ctx
}

// waitDockerEngine waits for initialization without rerunning any workflow mutation.
func waitDockerEngine(t *testing.T, ctx context.Context, engine string, req workflow.Request) {
	t.Helper()
	var last error
	for ctx.Err() == nil { // Keep readiness polling bounded by the fixture deadline.
		if engine == "postgres" { // Select the native engine protocol and its isolation rules.
			db, err := connectPostgres(ctx, req.Inputs["connection"], req)
			last = err
			if err == nil { // Validate this boundary before advancing the engine workflow.
				db.Close(ctx)
				return
			}
		} else {
			options, err := redisOptions(req.Inputs["connection"], req)
			if err != nil { // Require successful completion before using the returned engine state.
				t.Fatal(err)
			}
			client := redis.NewClient(options)
			last = client.Ping(ctx).Err()
			client.Close()
			if last == nil { // Validate this boundary before advancing the engine workflow.
				return
			}
		}
		// Poll only authentication readiness, never the uncertain create operation.
		select {
		case <-ctx.Done(): // Handle this command or readiness event with its native arguments.
		case <-time.After(200 * time.Millisecond): // Handle this command or readiness event with its native arguments.
		}
	}
	t.Fatalf("TLS fixture never became ready: %v", last)
}

// dockerContainer copies private keys before starting an unpublished-by-default disposable server.
func dockerContainer(t *testing.T, ctx context.Context, engine, directory string) (string, string) {
	t.Helper()
	port := "5432"
	args := []string{"create", "--label", "hostlier.test=tls-workflow", "-e", "POSTGRES_USER=hostlier", "-e", "POSTGRES_PASSWORD=fixture-admin"}
	command := "chown postgres /tmp/hostlier-tls/server.key; chmod 600 /tmp/hostlier-tls/server.key; exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/hostlier-tls/server.crt -c ssl_key_file=/tmp/hostlier-tls/server.key"
	if engine == "redis" { // Select the native engine protocol and its isolation rules.
		port = "6379"
		args = []string{"create", "--label", "hostlier.test=tls-workflow"}
		command = "chown redis /tmp/hostlier-tls/server.key; chmod 600 /tmp/hostlier-tls/server.key; exec docker-entrypoint.sh redis-server --port 0 --tls-port 6379 --tls-cert-file /tmp/hostlier-tls/server.crt --tls-key-file /tmp/hostlier-tls/server.key --tls-ca-cert-file /tmp/hostlier-tls/ca.crt --tls-auth-clients no --requirepass fixture-admin"
	}
	args = append(args, "-p", "127.0.0.1::"+port, resources.Images[engine], "sh", "-c", command)
	id := dockerCommand(t, ctx, args...)
	// Always remove test containers, including failed initialization cases.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		exec.CommandContext(cleanup, "docker", "rm", "-f", id).Run()
	})
	dockerCommand(t, ctx, "cp", directory, id+":/tmp/hostlier-tls")
	dockerCommand(t, ctx, "start", id)
	address := dockerCommand(t, ctx, "port", id, port+"/tcp")
	_, hostPort, err := net.SplitHostPort(address)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	return id, hostPort
}

// dockerCommand runs bounded fixture operations and emits diagnostics only for public setup failures.
func dockerCommand(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatalf("docker fixture command failed: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

// certificateFiles holds one temporary authority and matching localhost server keypair.
type certificateFiles struct{ directory, ca string }

// dockerCertificates generates fixture-only certificates without an external PKI or disabled verification.
func dockerCertificates(t *testing.T) certificateFiles {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	now := time.Now()
	authority := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Hostlier test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	ca, err := x509.CreateCertificate(rand.Reader, authority, authority, &key.PublicKey, key)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: authority.NotBefore, NotAfter: authority.NotAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	server, err := x509.CreateCertificate(rand.Reader, leaf, authority, &key.PublicKey, key)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca})
	writeCertificate(t, directory, "ca.crt", caPEM, 0644)
	writeCertificate(t, directory, "server.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server}), 0644)
	writeCertificate(t, directory, "server.key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded}), 0600)
	return certificateFiles{directory: directory, ca: string(caPEM)}
}

// writeCertificate preserves private key modes during Docker copy.
func writeCertificate(t *testing.T, directory, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), data, mode); err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
}

// assertInspection exercises response-loss recovery and checks that uncertain observations contain no outputs.
func assertInspection(t *testing.T, ctx context.Context, p Package, req workflow.Request, status workflow.InspectStatus) {
	t.Helper()
	req.Action = "inspect"
	response, err := p.Handle(ctx, req)
	// Only authoritative observations may resolve an interrupted package step.
	if err != nil || response.Inspection == nil || response.Inspection.Status != status || !response.Inspection.Valid() {
		t.Fatalf("inspection: expected %s, got %+v (%v)", status, response.Inspection, err)
	}
}
