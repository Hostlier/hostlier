package engines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/redis/go-redis/v9"
	"hostlier/pkg/workflow"
	"net/url"
	"reflect"
	"sort"
	"strings"
)

// inspectRedis reads ACL state and authenticates without issuing ACL SETUSER or application writes.
func inspectRedis(ctx context.Context, req workflow.Request, c Config, p payload) (workflow.Inspection, error) {
	u, err := connection("redis", req)
	// Require the reviewed verified transport before ACL discovery.
	if err != nil {
		return uncertain(), nil
	}
	options, err := redisOptions(u.String(), req)
	// Transport setup failures cannot establish resource absence.
	if err != nil {
		return uncertain(), nil
	}
	// Use the stable RESP2 alternating ACL representation for ownership verification.
	options.Protocol = 2
	admin := redis.NewClient(options)
	defer admin.Close()
	acl, err := admin.Do(ctx, "ACL", "GETUSER", p.Credentials.Username).Slice()
	// Redis nil is the authoritative absence response for this sole package effect.
	if err == redis.Nil {
		return workflow.Inspection{Status: workflow.NotStarted}, nil
	}
	// Exact ACL scope and generated password prove ownership and completion together.
	if err != nil || acl == nil || !validRedisACL(acl, req, c, p) {
		return uncertain(), nil
	}
	return redisObservedResult(ctx, req, c, p, u)
}

// validRedisACL rejects changed passwords, commands, channels, patterns and selectors.
func validRedisACL(raw []any, req workflow.Request, c Config, p payload) bool {
	acl := aclFields(raw)
	hash := sha256.Sum256([]byte(p.Credentials.Password))
	return safeRedisFlags(aclStrings(acl["flags"])) && reflect.DeepEqual(aclStrings(acl["passwords"]), []string{hex.EncodeToString(hash[:])}) && acl["keys"] == "~"+redisPrefix(req, c)+"*" && acl["channels"] == "" && len(aclStrings(acl["selectors"])) == 0 && sameCommands(acl["commands"])
}

// aclFields decodes Redis' alternating key/value ACL protocol without coercing unknown fields.
func aclFields(raw []any) map[string]any {
	fields := map[string]any{}
	// Only complete named pairs can contribute authoritative evidence.
	for i := 0; i+1 < len(raw); i += 2 {
		key, ok := raw[i].(string)
		// Unknown key encodings cannot establish an ACL field.
		if ok {
			fields[key] = raw[i+1]
		}
	}
	return fields
}

// aclStrings decodes ACL string lists while preserving malformed values as a mismatch.
func aclStrings(raw any) []string {
	list, ok := raw.([]any)
	// Missing and malformed fields must fail closed rather than match an empty list.
	if !ok {
		return []string{"invalid"}
	}
	result := make([]string, 0, len(list))
	// Preserve only valid string entries from the Redis wire representation.
	for _, entry := range list {
		value, ok := entry.(string)
		// One malformed entry invalidates the entire observed list.
		if !ok {
			return []string{"invalid"}
		}
		result = append(result, value)
	}
	return result
}

// sameCommands compares canonical command sets independently of Redis' output ordering.
func sameCommands(raw any) bool {
	value, ok := raw.(string)
	// Require an explicit command string from authoritative ACL discovery.
	if !ok {
		return false
	}
	observed, expected := strings.Fields(value), strings.Fields(redisCommands)
	sort.Strings(observed)
	sort.Strings(expected)
	return reflect.DeepEqual(observed, expected)
}

// redisCommands is the canonical least-privilege command set expected after ACL provisioning.
const redisCommands = "-@all +ping +get +set +del +exists +expire +pexpire +ttl +pttl +incr +incrby +decr +decrby +mget +mset +hget +hset +hdel +hgetall +lpush +rpush +lpop +rpop +lrange +sadd +srem +smembers +zadd +zrem +zrange"

// redisObservedResult verifies scoped login before exposing an observed ACL user's private URL.
func redisObservedResult(ctx context.Context, req workflow.Request, c Config, p payload, u *url.URL) (workflow.Inspection, error) {
	u.User = url.UserPassword(p.Credentials.Username, p.Credentials.Password)
	options, err := redisOptions(u.String(), req)
	// Require verified transport for scoped authentication too.
	if err != nil {
		return uncertain(), nil
	}
	app := redis.NewClient(options)
	defer app.Close()
	// A live authenticated PONG is required before the application consumes bindings.
	if app.Ping(ctx).Err() != nil {
		return uncertain(), nil
	}
	state, _ := json.Marshal(p)
	outputs := map[string]string{"url": u.String(), "keyPrefix": redisPrefix(req, c)}
	enrich(outputs, p.Credentials)
	exportTrust(outputs, req)
	delete(outputs, "database")
	return completed(workflow.Result{State: state, Outputs: outputs}), nil
}

// safeRedisFlags accepts Redis version defaults while rejecting disabled, passwordless or unrestricted users.
func safeRedisFlags(flags []string) bool {
	return reflect.DeepEqual(flags, []string{"on"}) || reflect.DeepEqual(flags, []string{"on", "sanitize-payload"})
}
