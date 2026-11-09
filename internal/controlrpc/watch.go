package controlrpc

import (
	"errors"
	"io"

	"connectrpc.com/connect"
)

// IsTransportInterruption distinguishes a lost stream from a gRPC status sent
// by the server. Callers decide whether their operation can be safely retried.
func IsTransportInterruption(err error) bool {
	if connect.IsWireError(err) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true // HTTP/2 closed without the required grpc-status trailer.
	}
	var reset streamReset
	if !errors.As(err, &reset) {
		return false
	}
	switch reset.Code {
	case 0x0, 0x2, 0x7, 0x8: // NO_ERROR, INTERNAL_ERROR, REFUSED_STREAM, CANCEL
		return true
	default:
		return false
	}
}

// net/http's HTTP/2 transport exposes its StreamError via errors.As to this
// three-field shape, including when wrapped by Connect. This keeps the
// standalone SDK on the standard library's transport.
type streamReset struct {
	StreamID uint32
	Code     uint32
	Cause    error
}

func (streamReset) Error() string { return "HTTP/2 stream reset" }
