package runtime

import (
	"encoding/json"
	"errors"
	"hostlier/internal/model"
	"os"
	"path/filepath"
)

// deploymentIntent identifies uncommitted single-container work without copying secret inputs.
type deploymentIntent struct {
	Application string `json:"application"`
	Operation   string `json:"operation"`
	Candidate   string `json:"candidate"`
}

// intentPath confines each app's recovery journal to its protected runtime state directory.
func (r *Runtime) intentPath(name string) string {
	return filepath.Join(r.options.StateDir, r.stateFile()+".intent-"+shortHash(name)+".json")
}

// readIntent validates the complete ownership identity before permitting crash cleanup.
func (r *Runtime) readIntent(name string) (*deploymentIntent, error) {
	path := r.intentPath(name)
	info, err := os.Lstat(path)
	// A normal completed deployment has no outstanding transition.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	// Recovery metadata cannot follow links or use special filesystem objects.
	if err != nil {
		return nil, err
	}
	// Bound the small public identity record and preserve private ownership permissions.
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, model.Err("RECOVERY_STATE", "Invalid deployment recovery journal")
	}
	raw, err := os.ReadFile(path)
	// Unreadable state never means absence of external effects.
	if err != nil {
		return nil, err
	}
	return r.decodeIntent(name, raw)
}

// decodeIntent binds persisted journal identity to its deterministic candidate name.
func (r *Runtime) decodeIntent(name string, raw []byte) (*deploymentIntent, error) {
	var intent deploymentIntent
	// Bind cleanup to an exact app and deterministic operation-owned candidate.
	if json.Unmarshal(raw, &intent) != nil || intent.Application != name || intent.Operation == "" || intent.Candidate != r.intentCandidate(name, intent.Operation) {
		return nil, model.Err("RECOVERY_STATE", "Invalid deployment recovery identity")
	}
	return &intent, nil
}

// intentCandidate derives the backend's deterministic workload name from approved operation identity.
func (r *Runtime) intentCandidate(name, operation string) string {
	// Kubernetes namespaces already contain the application and server ownership identity.
	if r.stateFilename == "runtime-k3s.json" {
		return "app-" + shortHash(operation)
	}
	return "hostlier-" + r.owner + "-" + name + "-" + shortHash(operation)
}

// beginIntent commits execution identity before creating or stopping any application workload.
func (r *Runtime) beginIntent(spec model.Project, operation string) error {
	// The separately implemented workload-group transaction has its own recovery contract.
	if spec.Version != 1 {
		return nil
	}
	raw, err := json.Marshal(deploymentIntent{Application: spec.Name, Operation: operation, Candidate: r.intentCandidate(spec.Name, operation)})
	// Never publish a partial identity record.
	if err != nil {
		return err
	}
	return atomicWrite(r.intentPath(spec.Name), raw)
}

// finishIntent removes a resolved journal only after runtime state and source retirement are durable.
func (r *Runtime) finishIntent(spec model.Project, s state) error {
	// Keep failed source retirement inspectable by a later approved deployment.
	if spec.Version != 1 || s.Apps[spec.Name].PreviousRuntimeID != "" {
		return nil
	}
	err := os.Remove(r.intentPath(spec.Name))
	// Repeated completion after a lost response is idempotent.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
