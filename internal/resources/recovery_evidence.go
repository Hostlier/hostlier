package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hostlier/internal/model"
	"regexp"
	"strings"
)

const postgresEvidenceSQL = `SELECT n.nspname||'.'||c.relname, query_to_xml(format('SELECT count(*),md5(coalesce(string_agg(row_to_json(t)::text,E''\n'' ORDER BY row_to_json(t)::text),'''')) FROM %I.%I t',n.nspname,c.relname),false,true,'') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','m') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' ORDER BY 1;
SELECT table_schema,table_name,column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_schema,table_name,ordinal_position;
SELECT n.nspname,c.relname,query_to_xml(format('SELECT last_value,is_called FROM %I.%I',n.nspname,c.relname),false,true,'') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='S' ORDER BY 1,2;`

const redisEvidenceScript = `local out={} for db=0,15 do redis.call('SELECT',db) local keys=redis.call('KEYS','*') table.sort(keys) for _,k in ipairs(keys) do local typ=redis.call('TYPE',k).ok local v={} if typ=='string' then v={redis.call('GET',k)} elseif typ=='list' then v=redis.call('LRANGE',k,0,-1) elseif typ=='set' then v=redis.call('SMEMBERS',k) table.sort(v) elseif typ=='zset' then v=redis.call('ZRANGE',k,0,-1,'WITHSCORES') elseif typ=='hash' then local fields=redis.call('HKEYS',k) table.sort(fields) for _,f in ipairs(fields) do table.insert(v,f) table.insert(v,redis.call('HGET',k,f)) end else return redis.error_reply('Hostlier verification does not support '..typ) end table.insert(out,{key=tostring(db)..':'..redis.sha1hex(k),hash=redis.sha1hex(typ..cjson.encode(v)),expires=redis.call('PEXPIRETIME',k)}) if #out>100000 then return redis.error_reply('Hostlier verification key limit') end end end if #out==0 then return '[]' end return cjson.encode(out)`

// evidenceCommands use authenticated native queries rather than treating container readiness as data recovery.
func evidenceCommands(engine string, c model.ResourceCredentials) ([]string, []string) {
	// Select the explicit recovery operation or native engine behavior.
	switch engine {
	// Handle only this supported recovery operation or retained state.
	case "postgres":
		base := []string{"psql", "-h", "127.0.0.1", "-U", c.Username, "-d", c.Database, "-v", "ON_ERROR_STOP=1", "-Atc"}
		return append(append([]string{}, base...), "SHOW server_version"), append(base, postgresEvidenceSQL)
	// Handle only this supported recovery operation or retained state.
	case "redis":
		return []string{"redis-cli", "--raw", "--user", c.Username, "INFO", "server"}, []string{"redis-cli", "--raw", "--user", c.Username, "EVAL", redisEvidenceScript, "0"}
	// Handle only this supported recovery operation or retained state.
	case "neo4j":
		base := []string{"cypher-shell", "-a", "bolt://localhost:7687", "-u", c.Username, "--format", "plain"}
		return []string{"neo4j", "--version"}, append(base, "CALL db.awaitIndexes(120); MATCH(n) RETURN id(n),labels(n),properties(n) ORDER BY id(n); MATCH(a)-[r]->(b) RETURN id(r),id(a),id(b),type(r),properties(r) ORDER BY id(r); SHOW CONSTRAINTS YIELD name,type,entityType,labelsOrTypes,properties RETURN name,type,entityType,labelsOrTypes,properties ORDER BY name;")
	}
	return nil, nil
}

// decodeEvidence retains only hashes and the observed native version in public recovery state.
func decodeEvidence(engine string, version, data []byte) (model.DatabaseEvidence, error) {
	out := model.DatabaseEvidence{Engine: engine}
	pattern := regexp.MustCompile(`[0-9]+\.[0-9]+(?:\.[0-9]+)?`)
	out.Version = pattern.FindString(string(version))
	// Empty or malformed results cannot pass a restore check.
	if out.Version == "" {
		return out, model.Err("RECOVERY_VERIFY", "Native verification returned no version or content")
	}
	sum := sha256.Sum256(data)
	out.Digest = hex.EncodeToString(sum[:])
	// Redis expiration uses absolute timestamps so verification can distinguish expiry from loss.
	if engine == "redis" {
		// Reject this request: Redis native verification failed or found unsupported data types.
		if json.Unmarshal(data, &out.Items) != nil {
			return out, model.Err("RECOVERY_VERIFY", "Redis native verification failed or found unsupported data types")
		}
	}
	return out, nil
}

// Evidence verifies the selected private database through its own authenticated query protocol.
func (m *Manager) Evidence(ctx context.Context, name string, c model.ResourceContext) (model.DatabaseEvidence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.find(name)
	// Never inspect a different owner's database.
	if err != nil {
		return model.DatabaseEvidence{}, err
	}
	versionCmd, dataCmd := evidenceCommands(item.Type, c.Credentials)
	version, err := m.execute(ctx, name, versionCmd, authEnv(item.Type, c.Credentials))
	// Query failures are not readiness evidence.
	if err != nil {
		return model.DatabaseEvidence{}, err
	}
	data, err := m.execute(ctx, name, dataCmd, authEnv(item.Type, c.Credentials))
	// Data-query failures prevent a verified recovery point.
	if err != nil {
		return model.DatabaseEvidence{}, err
	}
	return decodeEvidence(item.Type, version, data)
}

// Evidence performs the same verification inside the selected Kubernetes deployment.
func (m *K3sManager) Evidence(ctx context.Context, name string, c model.ResourceContext) (model.DatabaseEvidence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.find(name)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return model.DatabaseEvidence{}, err
	}
	versionCmd, dataCmd := evidenceCommands(item.Type, c.Credentials)
	// Keep asynchronous work bound to the controller lifetime.
	run := func(command []string) ([]byte, error) {
		args := []string{"exec", "-i", "deployment/engine", "-n", item.Network, "--", "sh", "-c", "read -r auth; export " + authEnv(item.Type, c.Credentials)[0][:strings.Index(authEnv(item.Type, c.Credentials)[0], "=")+1] + "\"$auth\"; exec \"$@\"", "hostlier"}
		args = append(args, command...)
		return m.run(ctx, args, []byte(c.Credentials.Password+"\n"))
	}
	version, err := run(versionCmd)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return model.DatabaseEvidence{}, err
	}
	data, err := run(dataCmd)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return model.DatabaseEvidence{}, err
	}
	return decodeEvidence(item.Type, version, data)
}

// Fence fails closed on Kubernetes until a storage/connection fencing mechanism can establish exclusive writers.
func (m *K3sManager) Fence(context.Context, string, bool, model.ResourceContext) error {
	return fmt.Errorf("upgrade fencing currently requires Docker; Kubernetes backups and verified restores remain supported")
}
