package engines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/redis/go-redis/v9"
	"hostlier/pkg/workflow"
)

// redisApply installs a fresh allowlisted ACL user restricted to one deterministic key namespace.
func redisApply(ctx context.Context, req workflow.Request, c Config, p payload) (workflow.Result, error) {
	u, err := connection("redis", req)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	options, err := redisOptions(u.String(), req)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	admin := redis.NewClient(options)
	defer admin.Close()
	prior, err := admin.Do(ctx, "ACL", "GETUSER", p.Credentials.Username).Result()
	if err != nil && !errors.Is(err, redis.Nil) { // Reject the operation: redis ownership lookup failed.
		return workflow.Result{}, errors.New("redis ownership lookup failed")
	}
	if prior != nil && len(req.Previous) == 0 { // Reject the operation: refusing existing untracked redis user.
		return workflow.Result{}, errors.New("refusing existing untracked redis user")
	}
	keyPrefix := redisPrefix(req, c)
	args := redisACLArguments(p, keyPrefix)

	if err = admin.Do(ctx, args...).Err(); err != nil { // Reject the operation: redis scoped ACL creation failed.
		return workflow.Result{}, errors.New("redis scoped ACL creation failed")
	}
	u.User = url.UserPassword(p.Credentials.Username, p.Credentials.Password)
	scoped, err := redisOptions(u.String(), req)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	app := redis.NewClient(scoped)
	defer app.Close()
	if err = app.Ping(ctx).Err(); err != nil { // Reject the operation: redis scoped authentication failed.
		return workflow.Result{}, errors.New("redis scoped authentication failed")
	}
	state, _ := json.Marshal(p)
	outputs := map[string]string{"url": u.String(), "keyPrefix": keyPrefix}
	enrich(outputs, p.Credentials)
	exportTrust(outputs, req)
	delete(outputs, "database")
	return workflow.Result{State: state, Outputs: outputs}, nil
}

// redisPrefix always includes dependency identity so configured labels cannot overlap other applications.
func redisPrefix(req workflow.Request, c Config) string {
	sum := sha256.Sum256([]byte(req.Key))
	return c.KeyPrefix + "hl:" + hex.EncodeToString(sum[:16]) + ":"
}

// redisOptions verifies TLS and installs provider trust before constructing either client.
func redisOptions(uri string, req workflow.Request) (*redis.Options, error) {
	options, err := redis.ParseURL(uri)
	if err != nil || options.DB != 0 { // Reject the operation: redis requires default database and valid TLS URL.
		return nil, errors.New("redis requires default database and valid TLS URL")
	}
	options.TLSConfig, err = trustedTLS(options.TLSConfig, req)
	if err != nil { // Require successful completion before using the returned engine state.
		return nil, err
	}
	return options, nil
}

// redisACLArguments shares the exact scoped command allowlist with read-only recovery verification.
func redisACLArguments(p payload, keyPrefix string) []any {
	args := []any{"ACL", "SETUSER", p.Credentials.Username, "reset", "on", ">" + p.Credentials.Password, "~" + keyPrefix + "*", "resetchannels"}
	// Each permission token is explicit and cannot include user-supplied commands.
	for _, command := range strings.Fields(redisCommands) {
		args = append(args, command)
	}
	return args
}
