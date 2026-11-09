package packagekit

import (
	"context"
	"hostlier/pkg/workflow"
)

// Handler implements a package lifecycle without depending on the controller binary.
type Handler interface {
	// Handle implements one action of the versioned package protocol.
	Handle(context.Context, workflow.Request) (workflow.Response, error)
}
