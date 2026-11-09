package controller

import (
	"hostlier/internal/model"
	"net/http"
)

// logTailRequest validates the public query before any runtime I/O.
func logTailRequest(r *http.Request) (*http.Request, error) {
	values, present := r.URL.Query()["tail"]
	// Omitted limits preserve each log endpoint's existing default.
	if !present {
		return r, nil
	}
	// Ambiguous duplicate limits are not silently resolved.
	if len(values) != 1 {
		return nil, model.Err("ARGUMENTS", "Supply one tail count")
	}
	count, err := model.ParseLogTail(values[0])
	// Reject invalid counts before reading application or controller output.
	if err != nil {
		return nil, err
	}
	return r.WithContext(model.WithLogTail(r.Context(), count)), nil
}

// boundedLogTail preserves the existing response byte cap after redaction and applies explicit line limits.
func boundedLogTail(text string, r *http.Request) string {
	// Runtime output remains bounded even when the requested line count is large.
	if len(text) > 65536 {
		text = text[len(text)-65536:]
	}
	// Resource backends without native tail support still honor the returned line limit.
	if r.URL.Query().Has("tail") {
		text = model.TailLines(text, model.LogTail(r.Context()))
	}
	return text
}
