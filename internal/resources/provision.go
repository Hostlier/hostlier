package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/model"
)

// ready waits for authenticated engine health within the configured deadline.
func (m *Manager) ready(ctx context.Context, spec model.ResourceSpec, c model.ResourceCredentials) error {
	ctx, cancel := context.WithTimeout(ctx, m.options.HealthTimeout)
	defer cancel()
	// Poll authenticated engine health until success or context timeout.
	for {
		current, err := m.inspect(ctx, spec.Name)
		// Require a verified owned container before changing or reporting its state.
		if err != nil {
			return err
		}
		// Reject this request: Resource container exited before authenticated readiness.
		if !current.State.Running {
			return model.Err("RESOURCE_EXIT", "Resource container exited before authenticated readiness")
		}
		command := readinessCommand(spec.Type, c)
		output, err := m.execute(ctx, spec.Name, command, authEnv(spec.Type, c))
		// Accept readiness only after an authenticated protocol success and Redis PONG.
		if err == nil && (spec.Type != "redis" || strings.TrimSpace(string(output)) == "PONG") {
			return nil
		}
		// Reject this request: Resource did not pass authenticated protocol readiness before timeout.
		if wait(ctx, 500*time.Millisecond) != nil {
			return model.Err("RESOURCE_READINESS", "Resource did not pass authenticated protocol readiness before timeout")
		}
	}
}

// Provision creates or resumes one owned resource with validated credentials.
func (m *Manager) Provision(ctx context.Context, input model.ResourceSpec, operation model.ResourceContext) (model.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	spec, err := m.approvedResourceSpec(ctx, input, operation.Credentials)
	// Validate the approved resource request before provisioning.
	if err != nil {
		return model.Resource{}, err
	}
	c := operation.Credentials
	state, err := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if err != nil {
		return model.Resource{}, err
	}
	// Require the owned private network and data volume before engine creation.
	if err = m.ensureResourceStorage(ctx, spec); err != nil {
		return model.Resource{}, err
	}
	item, err := m.prepareResourceRecord(spec, state)
	// Require a complete owned engine record before starting the resource.
	if err != nil {
		return model.Resource{}, err
	}
	current, err := m.inspect(ctx, spec.Name)
	// Handle absent engine objects through the resource reconciliation path.
	if code(err, "HTTP_404") {
		current, err = m.createResourceContainer(ctx, spec, operation)
	}
	// Require the selected owned engine container before starting it.
	if err != nil {
		return model.Resource{}, err
	}
	// Require engine startup before checking its authenticated readiness.
	if err = m.startResource(ctx, current, spec, c); err != nil {
		return model.Resource{}, err
	}
	// Require authenticated readiness before reporting the lifecycle operation complete.
	if err = m.ready(ctx, spec, c); err != nil {
		return model.Resource{}, err
	}
	item.RuntimeID = current.ID
	item.Status = "running"
	state[spec.Name] = item
	// Require durable resource state persistence before reporting completion.
	if err = m.save(state); err != nil {
		return model.Resource{}, err
	}
	return item.Resource, nil
}

// createResourceContainer pulls the approved image and builds a private owned engine container.
func (m *Manager) createResourceContainer(ctx context.Context, spec model.ResourceSpec, operation model.ResourceContext) (container, error) {
	c := operation.Credentials
	var err error

	// Require the immutable engine image to be available before container creation.
	if err = m.pullResourceImage(ctx, spec, operation); err != nil {
		return container{}, err
	}
	var env []string
	mount := "/data"
	port := 6379
	body := map[string]any{"Image": spec.Image, "Labels": m.labels(spec.Name)}
	// Select the native behavior for the requested engine or lifecycle operation.
	switch spec.Type {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		mount = "/var/lib/postgresql/data"
		port = 5432
		env = []string{"POSTGRES_USER=" + c.Username, "POSTGRES_PASSWORD=" + c.Password, "POSTGRES_DB=" + c.Database, "POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 --auth-local=scram-sha-256"}
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		port = 7687
		env = []string{"NEO4J_AUTH=" + c.Username + "/" + c.Password, "NEO4J_server_memory_heap_initial__size=256m", "NEO4J_server_memory_heap_max__size=512m", "NEO4J_server_memory_pagecache_size=256m"}
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		body["Cmd"] = []string{"redis-server", "/tmp/hostlier-redis.conf"}
	}
	body["Env"] = env
	body["ExposedPorts"] = map[string]any{fmt.Sprintf("%d/tcp", port): map[string]any{}}
	body["HostConfig"] = map[string]any{"NetworkMode": m.network(spec.Name), "RestartPolicy": map[string]string{"Name": "unless-stopped"}, "Mounts": []any{map[string]string{"Type": "volume", "Source": m.volume(spec.Name), "Target": mount}}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "10m", "max-file": "3"}}}
	// Recreated resource containers retain approved CPU and memory limits.
	if spec.CPU != "" || spec.Memory != "" {
		// Copy allocation fields into the recreated container without altering mounts.
		for key, value := range dockerAllocation(spec) {
			body["HostConfig"].(map[string]any)[key] = value
		}
	}
	body["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{m.network(spec.Name): map[string]any{"Aliases": []string{m.container(spec.Name)}}}}
	// Require the owned Docker API request to complete before using its result.
	if _, err = m.api(ctx, "POST", "/containers/create?name="+url.QueryEscape(m.container(spec.Name)), body, nil); err != nil {
		return container{}, err
	}
	current, err := m.inspect(ctx, spec.Name)
	// Require a verified owned container before changing or reporting its state.
	if err != nil {
		return container{}, err
	}
	return current, nil
}

// startResource installs private Redis authentication before starting a stopped container.
func (m *Manager) startResource(ctx context.Context, current container, spec model.ResourceSpec, c model.ResourceCredentials) error {
	var err error

	// Reject this request: Existing image does not match approved resource pin.
	if current.Config.Image != spec.Image {
		return model.Err("RESOURCE_VERSION", "Existing image does not match approved resource pin")
	}
	// Install startup configuration and start the container only when it is stopped.
	if !current.State.Running {
		// Prepare an authenticated AOF-enabled Redis startup configuration.
		if spec.Type == "redis" {
			configuration := redisConfig(c, true)
			// Install the authenticated Redis startup configuration before starting its container.
			if err = m.upload(ctx, current.ID, "/tmp", "hostlier-redis.conf", []byte(configuration), 999); err != nil {
				return err
			}
		}
		// Require the owned Docker API request to complete before using its result.
		if _, err = m.api(ctx, "POST", "/containers/"+current.ID+"/start", nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// pullResourceImage reports progress and rejects per-layer Docker pull errors.
func (m *Manager) pullResourceImage(ctx context.Context, spec model.ResourceSpec, operation model.ResourceContext) error {
	// Publish provisioning progress only when an event consumer is configured.
	if operation.OnEvent != nil {
		operation.OnEvent(model.Event{Level: "info", Step: "resource-pull", Message: "Pulling approved immutable resource image"})
	}
	output, pullErr := m.api(ctx, "POST", "/images/create?fromImage="+url.QueryEscape(spec.Image), nil, nil)
	// Require the owned Docker API request to complete before using its result.
	if pullErr != nil {
		return pullErr
	}
	// Inspect each Docker pull progress record for an embedded image download error.
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		var progress struct{ Error string }
		// Reject this request: Resource image pull failed.
		if json.Unmarshal(line, &progress) == nil && progress.Error != "" {
			return model.Err("RESOURCE_IMAGE", "Resource image pull failed")
		}
	}
	return nil
}

// readinessCommand probes each engine through its authenticated native protocol.
func readinessCommand(engine string, c model.ResourceCredentials) []string {
	var command []string
	// Select the native behavior for the requested engine or lifecycle operation.
	switch engine {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		command = []string{"psql", "-h", "127.0.0.1", "-U", c.Username, "-d", c.Database, "-Atc", "SELECT 1"}
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		command = []string{"redis-cli", "--user", c.Username, "PING"}
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		command = []string{"cypher-shell", "-a", "bolt://127.0.0.1:7687", "-u", c.Username, "RETURN 1;"}
	}
	return command
}

// ensureResourceStorage creates or verifies only the named owned network and volume.
func (m *Manager) ensureResourceStorage(ctx context.Context, spec model.ResourceSpec) error {
	var err error

	// Require owned network and volume prerequisites before engine creation.
	if err = m.ensure(ctx, "networks", spec.Name, m.network(spec.Name), map[string]any{"Name": m.network(spec.Name), "Driver": "bridge", "Internal": true, "Labels": m.labels(spec.Name)}); err != nil {
		return err
	}
	// Require owned network and volume prerequisites before engine creation.
	if err = m.ensure(ctx, "volumes", spec.Name, m.volume(spec.Name), map[string]any{"Name": m.volume(spec.Name), "Labels": m.labels(spec.Name)}); err != nil {
		return err
	}
	return nil
}

// prepareResourceRecord persists provisioning metadata before creating the engine container.
func (m *Manager) prepareResourceRecord(spec model.ResourceSpec, state map[string]record) (record, error) {
	var err error

	item, ok := state[spec.Name]
	// Reject this request: Database is reserved by a recovery workflow.
	if item.RecoveryFence != "" {
		return record{}, model.Err("RECOVERY_FENCE", "Database is reserved by a recovery workflow")
	}
	// Persist a new provisioning record only when this resource has no retained record.
	if !ok {
		item = record{Resource: model.Resource{Name: spec.Name, Type: spec.Type, Network: m.network(spec.Name), Status: "provisioning"}, Spec: spec, Volume: m.volume(spec.Name)}
		state[spec.Name] = item
		// Require durable resource state persistence before reporting completion.
		if err = m.save(state); err != nil {
			return record{}, err
		}
	}
	return item, nil
}

// approvedResourceSpec validates the immutable plan and credentials before storage changes.
func (m *Manager) approvedResourceSpec(ctx context.Context, input model.ResourceSpec, c model.ResourceCredentials) (model.ResourceSpec, error) {
	plan, err := m.Plan(ctx, input)
	// Require a valid read-only resource plan before using its normalized specification.
	if err != nil {
		return model.ResourceSpec{}, err
	}
	spec := plan.Spec
	// Validate engine credentials before issuing authenticated commands.
	if err = validateCredentials(spec.Type, c); err != nil {
		return model.ResourceSpec{}, err
	}
	return spec, nil
}
