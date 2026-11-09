// Package providers implements optional direct managed-provider workflows.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// Client only sends credentials to the fixed official Aiven API origin.
type Client struct{ Transport http.RoundTripper }

// call bounds all provider responses and never retries mutations or follows redirects.
func (c Client) call(ctx context.Context, method, path, token string, body any, out any) (int, error) {
	raw, err := json.Marshal(body)
	// Encoding failures must occur before any request is sent.
	if err != nil {
		return 0, errors.New("invalid provider request")
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.aiven.io/v1"+path, bytes.NewReader(raw))
	// Stop before producing state when the provider operation fails.
	if err != nil {
		return 0, errors.New("invalid provider request")
	}
	req.Header.Set("Authorization", "aivenv1 "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: c.Transport, Timeout: 30 * time.Second, CheckRedirect: noRedirect}
	response, err := client.Do(req)
	// Provider messages can contain credentials; return fixed diagnostics only.
	if err != nil {
		return 0, errors.New("provider request failed; mutation outcome may require reconciliation")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	// Stop before producing state when the provider operation fails.
	if err != nil || len(data) > 4*1024*1024 {
		return response.StatusCode, errors.New("invalid provider response")
	}
	// Validate provider state before advancing the workflow.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, errors.New("provider request rejected")
	}
	// Validate provider state before advancing the workflow.
	if out != nil && json.Unmarshal(data, out) != nil {
		return response.StatusCode, errors.New("invalid provider response")
	}
	return response.StatusCode, nil
}

// noRedirect prevents credential forwarding to any secondary origin.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
