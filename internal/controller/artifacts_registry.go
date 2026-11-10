package controller

import (
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"time"

	"hostlier/internal/artifact"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// registryImagePreparer stages image content without starting Docker containers or Kubernetes workloads.
type registryImagePreparer interface {
	PrepareRegistryImage(context.Context, string, string) (artifact.Result, error)
}

// pullRegistryArtifact releases dispatch authority only while preparing inert private archive bytes.
func (c *Controller) pullRegistryArtifact(r *http.Request, store artifact.Store, actor string) (any, error) {
	image, err := registryPreparationSource(r)
	// Format and platform constraints are checked before reading server credentials.
	if err != nil {
		return nil, err
	}
	preparer, ok := c.opts.Runtime.(registryImagePreparer)
	if !ok { // Unsupported runtime capabilities are distinct from registry connection failures.
		return nil, model.Err("UNSUPPORTED_RUNTIME", "Runtime does not support controller image preparation")
	}
	if c.closed || c.registryPulls >= 2 { // Bound download concurrency without holding up status or approvals.
		return nil, model.Err("UNAVAILABLE", "Controller image preparation is busy or shutting down")
	}
	if err := store.CheckAvailableSpace(0); err != nil { // Local upload cannot fix insufficient server artifact capacity.
		return nil, err
	}
	dir, err := os.MkdirTemp(store.Root, "registry-pull-")
	if err != nil { // Preparation cannot use an unsafe shared or caller-chosen directory.
		return nil, model.Err("ARTIFACT_WRITE", "Cannot create controller image preparation directory")
	}
	defer os.RemoveAll(dir)
	// Leave room inside the client's ten-minute command deadline to return a typed fallback result.
	ctx, cancel := context.WithTimeout(r.Context(), min(c.opts.LongOperationTimeout, 9*time.Minute))
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	c.registryPulls++
	defer func() { c.registryPulls-- }() // Cleanup must never remove an active preparation tree.
	hadDevice := c.state.Clients[actor] != nil
	receipt, err := c.downloadRegistryArchive(ctx, preparer, image, dir)
	// Check trust again before publishing bytes into the shared artifact store.
	if err := c.registryPreparationAuthority(r, actor, hadDevice); err != nil {
		return nil, err
	}
	if err != nil { // Credential and registry failures are typed; raw helper/provider diagnostics are never returned.
		return nil, registryPreparationError(ctx, r.Context(), err)
	}
	// Publication shares the dispatch lock with CLI chunk writes, commits and cleanup.
	return store.Import(ctx, receipt)
}

// downloadRegistryArchive keeps network and private archive work outside the controller's state lock.
func (c *Controller) downloadRegistryArchive(ctx context.Context, preparer registryImagePreparer, image, dir string) (artifact.Result, error) {
	c.mu.Unlock()
	defer c.mu.Lock()
	return preparer.PrepareRegistryImage(ctx, image, dir)
}

// registryPreparationError distinguishes a server registry timeout from a lost or cancelled CLI connection.
func registryPreparationError(attempt, request context.Context, err error) error {
	// Only a still-connected client can receive a typed registry failure and safely perform local fallback.
	if errors.Is(err, context.DeadlineExceeded) && attempt.Err() == context.DeadlineExceeded && request.Err() == nil {
		return model.Err("IMAGE_PULL", "Controller registry download timed out")
	}
	return err
}

// registryPreparationSource decodes only registry coordinates and an optional receiver platform constraint.
func registryPreparationSource(r *http.Request) (string, error) {
	var source artifact.RegistryImage
	// Callers may name an image and platform constraint, never credentials or local filesystem paths.
	if err := decode(r, &source); err != nil {
		return "", err
	}
	if err := config.ValidateRegistryImage(source.Image, source.Architecture); err != nil { // Reject malformed requests before server login lookup.
		return "", err
	}
	if source.Architecture != "" && source.Architecture != runtime.GOARCH { // Stale receiver discovery must not choose an incompatible image.
		return "", model.Err("ARTIFACT_PLATFORM", "Image architecture does not match controller")
	}
	return source.Image, nil
}

// registryPreparationAuthority rechecks trust after slow work without replaying a signed request nonce.
func (c *Controller) registryPreparationAuthority(r *http.Request, actor string, hadDevice bool) error {
	if c.closed { // Shutdown must not publish or save new state after closing the controller store.
		return model.Err("UNAVAILABLE", "Controller is shutting down")
	}
	if err := c.checkHostAuthority(r); err != nil { // A delegated host binding may have changed while the registry was slow.
		return err
	}
	if !hadDevice { // Local administration and pinned fleet authority were authenticated by their own listener.
		return nil
	}
	current := c.state.Clients[actor]
	if current == nil || current.Revoked { // Revoked credentials cannot receive a preparation result.
		return model.Err("UNAUTHORIZED", "Controller access was revoked during image preparation")
	}
	return c.checkScopedRoute(r, current)
}
