package resources

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"

	"hostlier/internal/model"
)

// helper mounts only the selected owned data volume, publishes no ports, and
// cannot access any network. It never receives database credentials in argv.
func (m *Manager) helper(ctx context.Context, item record, entrypoint, command []string, input []byte, inputName string, uid int, outputFile string) ([]byte, error) {
	name := m.container(item.Name) + "-maintenance"
	// Refuse maintenance when a stale helper cannot be safely removed.
	if err := m.removeMaintenanceContainer(ctx, name, item.Name); err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	body := map[string]any{"Image": item.Spec.Image, "Hostname": "localhost", "Labels": m.labels(item.Name), "Entrypoint": entrypoint, "Cmd": command, "User": fmt.Sprintf("%d:%d", uid, uid), "HostConfig": map[string]any{"NetworkMode": "none", "Mounts": []any{map[string]string{"Type": "volume", "Source": item.Volume, "Target": "/data"}}}}
	// Require the owned Docker API request to complete before using its result.
	if _, err := m.api(ctx, "POST", "/containers/create?name="+name, body, &created); err != nil {
		return nil, err
	}
	defer m.api(context.Background(), "DELETE", "/containers/"+created.ID+"?force=true", nil, nil)
	// Upload restore bytes before starting the offline maintenance command.
	if input != nil {
		// Install the supplied restore input before starting the offline helper.
		if err := m.upload(ctx, created.ID, "/tmp", inputName, input, uid); err != nil {
			return nil, err
		}
	}
	// Require the owned Docker API request to complete before using its result.
	if _, err := m.api(ctx, "POST", "/containers/"+created.ID+"/start", nil, nil); err != nil {
		return nil, err
	}
	var status struct{ StatusCode int }
	// Require the owned Docker API request to complete before using its result.
	if _, err := m.api(ctx, "POST", "/containers/"+created.ID+"/wait?condition=not-running", nil, &status); err != nil {
		return nil, err
	}
	// Reject this request: Offline resource maintenance failed.
	if status.StatusCode != 0 {
		// Emit maintenance diagnostics only when a diagnostic consumer was configured.
		if m.options.OnDiagnostic != nil {
			// Forward bounded helper diagnostics only when log retrieval succeeds.
			if logs, logErr := m.api(ctx, "GET", "/containers/"+created.ID+"/logs?stdout=true&stderr=true", nil, nil); logErr == nil {
				stdout, stderr, _ := demultiplex(logs)
				m.options.OnDiagnostic(string(stdout) + string(stderr))
			}
		}
		return nil, model.Err("RESOURCE_BACKUP", "Offline resource maintenance failed")
	}
	return m.maintenanceOutput(ctx, created.ID, outputFile)
}

// removeMaintenanceContainer removes only an owned stale maintenance container.
func (m *Manager) removeMaintenanceContainer(ctx context.Context, name, resourceName string) error {
	var existing struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	// Verify ownership and remove a stale helper only when its lookup finds a container.
	if _, err := m.api(ctx, "GET", "/containers/"+name+"/json", nil, &existing); err == nil {
		// Reject this request: Maintenance container belongs to another owner.
		if !m.owned(existing.Config.Labels, resourceName) {
			return model.Err("RESOURCE_OWNERSHIP", "Maintenance container belongs to another owner")
		}
		// Require the owned Docker API request to complete before using its result.
		if _, err = m.api(ctx, "DELETE", "/containers/"+existing.ID+"?force=true", nil, nil); err != nil {
			return err
		}
	} else if !code(err, "HTTP_404") { // Propagate failures other than an absent stale maintenance helper.
		return err
	}
	return nil
}

// maintenanceOutput reads an archived dump or demultiplexed maintenance logs.
func (m *Manager) maintenanceOutput(ctx context.Context, id, outputFile string) ([]byte, error) {
	// Retrieve the tar archive when offline maintenance produced a native dump file.
	if outputFile != "" {
		raw, err := m.api(ctx, "GET", "/containers/"+id+"/archive?path=/tmp/"+outputFile, nil, nil)
		// Require the owned Docker API request to complete before using its result.
		if err != nil {
			return nil, err
		}
		reader := tar.NewReader(bytes.NewReader(raw))
		header, err := reader.Next()
		// Reject this request: Invalid offline dump archive.
		if err != nil || header.Name != outputFile || header.Size > MaxBackupBytes {
			return nil, model.Err("RESOURCE_BACKUP", "Invalid offline dump archive")
		}
		return io.ReadAll(io.LimitReader(reader, MaxBackupBytes+1))
	}
	data, err := m.api(ctx, "GET", "/containers/"+id+"/logs?stdout=true&stderr=true", nil, nil)
	// Require the owned Docker API request to complete before using its result.
	if err != nil {
		return nil, err
	}
	stdout, _, err := demultiplex(data)
	return stdout, err
}
