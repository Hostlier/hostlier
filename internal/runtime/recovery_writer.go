package runtime

import (
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"strconv"
)

// stopPreviousWriter transfers ownership only after observing the prior writer or recovered absence.
func (d *dockerDeployment) stopPreviousWriter() error {
	r := d.runtime
	d.event("stop-old", "Stopping previous volume writer")
	previous, err := r.container(d.ctx, d.old.RuntimeID, d.spec.Name)
	// Only a restored controller at its original path can forget an authoritatively absent old writer.
	if r.recoveredMissingWriter(err) {
		d.old.RuntimeID = ""
		d.old.PreviousRuntimeID = ""
		d.old.Anchor = ""
		d.old.Members = nil
		return nil
	}
	// Ownership mismatch and all other inspection failures remain fatal.
	if err != nil {
		return err
	}
	// Stop must succeed before rollback is authorized to restart the prior writer.
	if err = r.api(d.ctx, "POST", "/containers/"+d.old.RuntimeID+"/stop?t="+strconv.Itoa(model.GraceSeconds(d.old.Spec)), nil, nil, nil); err != nil {
		return err
	}
	d.oldStopped = previous.State.Running
	return r.memberAction(d.ctx, d.old, "stop")
}

// recoveredMissingWriter fences clean-server redeploy to a verified restored state directory.
func (r *Runtime) recoveredMissingWriter(err error) bool {
	// Absence is the only daemon response that proves the old writer cannot still be using this host's volume.
	if !hasCode(err, "HTTP_404") {
		return false
	}
	_, markerErr := os.Lstat(filepath.Join(r.options.StateDir, "recovery-origin.json"))
	return markerErr == nil && model.ValidateRecoveryLocation(r.options.StateDir) == nil
}
