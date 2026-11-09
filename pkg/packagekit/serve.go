// Package packagekit provides the bounded executable protocol shared by installed packages.
package packagekit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

// MaxProtocolBytes is the common request and response limit for protocol v1.
const MaxProtocolBytes = 4 * 1024 * 1024

// Serve accepts exactly one bounded request and writes one validated private response.
func Serve(ctx context.Context, handler Handler, in io.Reader, out io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(in, MaxProtocolBytes+1))
	// Bound protocol data before decoding its allocation-controlled fields.
	if err != nil || len(data) > MaxProtocolBytes {
		return errors.New("invalid package request")
	}
	req, err := decode(data)
	// A malformed request must never invoke package code.
	if err != nil {
		return err
	}
	response, err := handler.Handle(ctx, req)
	// Package errors never publish partial success state.
	if err != nil {
		return err
	}
	// Enforce action-specific response shapes at the executable boundary.
	if !validResponse(req.Action, response) {
		return errors.New("invalid package response")
	}
	encoded, err := json.Marshal(response)
	// Stop on storage failure without returning partial private content.
	if err != nil || len(encoded)+1 > MaxProtocolBytes {
		return errors.New("invalid package response")
	}
	encoded = append(encoded, '\n')
	_, err = out.Write(encoded)
	return err
}
