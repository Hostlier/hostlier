package fleet

import (
	"context"
	"net"
	"sync"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ConnectProcedure carries a reverse TLS/HTTP2 connection in bounded gRPC frames.
// HTTP2 multiplexing preserves independent command and watch cancellation.
const ConnectProcedure = "/hostlier.fleet.v1.Fleet/Connect"
const FrameLimit = 64 << 10

// StreamConn bridges a gRPC stream to a full-duplex connection. Closing either
// side or cancelling ctx interrupts blocked pipe operations without buffering
// unbounded peer data. Before returning from the RPC, the caller must cancel
// its transport, close the connection, and call the returned wait function.
func StreamConn(ctx context.Context, send func(*wrapperspb.BytesValue) error, receive func() (*wrapperspb.BytesValue, error)) (net.Conn, <-chan struct{}, func()) {
	conn, bridge := net.Pipe()
	done := make(chan struct{})
	var once sync.Once
	var pumps sync.WaitGroup
	pumps.Add(2)
	closed := func() { bridge.Close(); once.Do(func() { close(done) }) }
	stop := context.AfterFunc(ctx, closed)
	go func() {
		defer pumps.Done()
		defer closed()
		defer stop()
		buffer := make([]byte, FrameLimit)
		for {
			n, err := bridge.Read(buffer)
			if n > 0 {
				if send(wrapperspb.Bytes(buffer[:n])) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer pumps.Done()
		defer closed()
		for {
			frame, err := receive()
			if err != nil || len(frame.GetValue()) == 0 || len(frame.GetValue()) > FrameLimit {
				return
			}
			if _, err = bridge.Write(frame.Value); err != nil {
				return
			}
		}
	}()
	return conn, done, pumps.Wait
}
