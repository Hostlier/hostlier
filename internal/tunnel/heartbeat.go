package tunnel

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// heartbeat bounds silent network partitions using standard WebSocket control frames.
func heartbeat(ctx context.Context, done <-chan struct{}, ws *websocket.Conn, closeConnection func()) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	// Control frames do not buffer or reinterpret the application's HTTP byte stream.
	for {
		// Wake only for lease cancellation, pipe completion or the heartbeat interval.
		select {
		// Lease cancellation revokes heartbeat activity together with application traffic.
		case <-ctx.Done():
			return
		// A disconnected pipe cannot leave a heartbeat worker waiting for its next interval.
		case <-done:
			return
		// Periodically prove that the same authenticated peer remains reachable.
		case <-ticker.C:
			ping, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := ws.Ping(ping)
			cancel()
			// A silent partition must release its slot and allow a fresh outbound connection.
			if err != nil {
				closeConnection()
				return
			}
		}
	}
}
