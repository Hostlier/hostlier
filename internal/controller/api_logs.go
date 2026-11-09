package controller

import (
	"hostlier/internal/model"
	"hostlier/internal/store"
	"net/http"
)

// apiLogs routes authenticated requests for execution events.
func (c *Controller) apiLogs(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// Expose the bounded controller event inventory on the exact collection route.
	if method == "GET" && len(p) == 2 {
		r, err := logTailRequest(r)
		// Reject invalid event limits before scanning retained logs.
		if err != nil {
			return nil, err
		}
		query := store.CollectionQuery{Operation: r.URL.Query().Get("operationId")}
		if r.URL.Query().Has("tail") {
			query.Tail = true
			query.Limit = model.LogTail(r.Context())
		}
		entries, err := collectionValues[LogEntry](r.Context(), c.store, "events", query)
		return map[string]any{"entries": entries}, err
	}

	return endpointNotFound()
}
