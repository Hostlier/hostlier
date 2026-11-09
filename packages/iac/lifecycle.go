package iac

import (
	"context"
	"encoding/json"
	"errors"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/packagekit/protected"
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"time"
)

// journal records uncertain intent before the first native mutation.
type journal struct {
	Complete bool
	Receipt  protected.Handle
}

// journalPath is derived solely from the authenticated operation digest.
func journalPath(e Execution) string {
	return filepath.Join(e.Backend, "operation-"+e.Operation+".json")
}

// persist exclusively creates and fsyncs the durable intent before native apply.
func persist(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return errors.New("operation already attempted; inspect before recovery")
	}
	_, err = f.Write(data)
	// Stop this phase when private storage or native execution cannot be verified.
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	// Stop this phase when private storage or native execution cannot be verified.
	if err == nil {
		err = closeErr
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

// syncDirectory makes receipt renames and intent creation durable across process loss.
func syncDirectory(path string) error {
	f, err := os.Open(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// complete atomically replaces an uncertain journal with its encrypted result handle.
func complete(e Execution, h protected.Handle) error {
	raw, _ := json.Marshal(journal{Complete: true, Receipt: h})
	path := journalPath(e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err := persist(path+".complete", raw); err != nil {
		return err
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err := os.Rename(path+".complete", path); err != nil {
		return err
	}
	return syncDirectory(e.Backend)
}

// apply rejects stale state and any previously attempted operation before engine execution.
func (p *Package) apply(ctx context.Context, c packagekit.Call[Config, State], prepared Prepared) (packagekit.Result[State, Outputs], error) {
	store, e, clean, err := p.session(c)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Result[State, Outputs]{}, err
	}
	defer clean()
	unlock, err := store.Lock(e.Owner)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Result[State, Outputs]{}, err
	}
	defer unlock()
	binding, err := p.binding(e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Result[State, Outputs]{}, err
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if binding != prepared.Binding {
		return packagekit.Result[State, Outputs]{}, errors.New("approved engine identity changed; replan required")
	}
	saved, err := store.Load(prepared.Artifact, binding)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Result[State, Outputs]{}, err
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err = persist(journalPath(e), []byte(`{"Complete":false}`)); err != nil {
		return packagekit.Result[State, Outputs]{}, err
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err = p.engine.Apply(ctx, e, saved); err != nil {
		return packagekit.Result[State, Outputs]{}, err
	}
	result, err := p.result(ctx, e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return result, err
	}
	raw, _ := json.Marshal(receipt{State: result.State, Outputs: result.Outputs})
	handle, err := store.Save(prepared.Binding, raw, 7*24*time.Hour)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return result, err
	}
	return result, complete(e, handle)
}

// result validates bundle-local output allowlists before writing a successful receipt.
func (p *Package) result(ctx context.Context, e Execution) (packagekit.Result[State, Outputs], error) {
	out, err := p.engine.Outputs(ctx, e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Result[State, Outputs]{}, err
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if !allowedOutputs(out, e.Installed.Outputs) {
		return packagekit.Result[State, Outputs]{}, errors.New("undeclared native output")
	}
	revision, err := p.engine.Revision(e)
	return packagekit.Result[State, Outputs]{State: State{Owner: e.Owner, Operation: e.Operation, Revision: revision}, Outputs: out}, err
}

// allowedOutputs rejects undeclared and ambiguously classified native outputs.
func allowedOutputs(o Outputs, names []string) bool {
	allowed := map[string]bool{}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, name := range names {
		allowed[name] = true
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for name := range o.Public {
		// Reject an identity or visibility mismatch before publishing the operation result.
		if !allowed[name] {
			return false
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if _, ok := o.Private[name]; ok {
			return false
		}
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for name := range o.Private {
		// Reject an identity or visibility mismatch before publishing the operation result.
		if !allowed[name] {
			return false
		}
	}
	return true
}

// inspect never invokes apply or refresh and refuses to infer success after an interrupted mutation.
func (p *Package) inspect(ctx context.Context, c packagekit.Call[Config, State], prepared Prepared) (packagekit.Observation[State, Outputs], error) {
	store, e, clean, err := p.session(c)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Observation[State, Outputs]{}, err
	}
	defer clean()
	unlock, err := store.Lock(e.Owner)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Observation[State, Outputs]{Status: workflow.Uncertain}, nil
	}
	defer unlock()
	return p.observeReceipt(store, e, prepared)
}

// observeReceipt publishes only a persisted success whose recorded native revision still matches.
func (p *Package) observeReceipt(store *protected.Store, e Execution, prepared Prepared) (packagekit.Observation[State, Outputs], error) {
	uncertain := packagekit.Observation[State, Outputs]{Status: workflow.Uncertain}
	raw, err := os.ReadFile(journalPath(e))
	// Stop this phase when private storage or native execution cannot be verified.
	if os.IsNotExist(err) {
		binding, bindErr := p.binding(e)
		// Stop this phase when private storage or native execution cannot be verified.
		if bindErr == nil && binding == prepared.Binding {
			return packagekit.Observation[State, Outputs]{Status: workflow.NotStarted}, nil
		}
		return uncertain, nil
	}
	var j journal
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil || json.Unmarshal(raw, &j) != nil || !j.Complete {
		return uncertain, nil
	}
	return p.recoverReceipt(store, e, prepared, j.Receipt)
}

// recoverReceipt authenticates encrypted completion before checking the current native checkpoint.
func (p *Package) recoverReceipt(store *protected.Store, e Execution, prepared Prepared, h protected.Handle) (packagekit.Observation[State, Outputs], error) {
	uncertain := packagekit.Observation[State, Outputs]{Status: workflow.Uncertain}
	raw, err := store.Load(h, prepared.Binding)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return uncertain, nil
	}
	var r receipt
	// Reject an identity or visibility mismatch before publishing the operation result.
	if json.Unmarshal(raw, &r) != nil {
		return uncertain, nil
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if !p.sameOutcome(e, prepared, r) {
		return uncertain, nil
	}

	result := packagekit.Result[State, Outputs]{State: r.State, Outputs: r.Outputs}
	return packagekit.Observation[State, Outputs]{Status: workflow.Completed, Result: &result}, nil
}

// sameOutcome refuses stale, cross-owner and changed-toolchain completion receipts.
func (p *Package) sameOutcome(e Execution, prepared Prepared, r receipt) bool {
	// Reject an identity or visibility mismatch before publishing the operation result.
	if r.State.Owner != e.Owner || r.State.Operation != e.Operation {
		return false
	}
	revision, err := p.engine.Revision(e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil || revision != r.State.Revision {
		return false
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if _, err = e.Installed.Bundle.Digest(); err != nil {
		return false
	}
	return hash(e.Installed) == prepared.Binding.Bundle && executableDigest() == prepared.Binding.Executable
}

// unresolved refuses a new plan while any previous native mutation for this owner lacks proof of completion.
func unresolved(e Execution) error {
	paths, err := filepath.Glob(filepath.Join(e.Backend, "operation-*.json"))
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, path := range paths {
		info, err := os.Lstat(path)
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			return errors.New("invalid native operation journal")
		}
		raw, err := os.ReadFile(path)
		var j journal
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil || json.Unmarshal(raw, &j) != nil || !j.Complete {
			return errors.New("unresolved native operation; investigate before replanning")
		}
	}
	return nil
}
