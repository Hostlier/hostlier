package client

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"hostlier/internal/model"
)

// readStatusFrames reuses bounded SSE framing without accepting incomplete snapshots after disconnect.
func readStatusFrames(reader io.Reader, consume func(StatusEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxOperationFrameBytes+1)
	frame := operationFrame{}
	// Chunk boundaries are independent of complete SSE frame boundaries.
	for scanner.Scan() {
		line := scanner.Text()
		// A blank line commits a full JSON replacement snapshot.
		if line == "" {
			// Consumer errors remain distinct from transport EOF.
			if err := consumeStatusFrame(frame, consume); err != nil {
				return err
			}
			frame = operationFrame{}
			continue
		}
		// Bound the aggregate frame, including comments and multiple data lines.
		if err := frame.addLine(line); err != nil {
			return err
		}
	}
	// Oversized frames are permanent protocol violations, not reconnectable transport loss.
	if scanner.Err() == bufio.ErrTooLong {
		return model.Err("EVENT_LIMIT", "Status snapshot exceeds response limit")
	}
	return io.EOF
}

// consumeStatusFrame validates replacement identity before making it visible to a UI cache.
func consumeStatusFrame(frame operationFrame, consume func(StatusEvent) error) error {
	// Heartbeat comments contain no data and never replace cached state.
	if len(frame.data) == 0 {
		return nil
	}
	var snapshot model.StatusSnapshot
	// Snapshot identity and bounded inventory are required even on an authenticated connection.
	if frame.event != "snapshot" || json.Unmarshal([]byte(strings.Join(frame.data, "\n")), &snapshot) != nil || snapshot.ID == "" || len(snapshot.Applications) > 128 || len(snapshot.Operations) > 128 {
		return model.Err("EVENT_PROTOCOL", "Invalid status snapshot")
	}
	// Local delivery is acknowledged only when the consumer accepts the complete snapshot.
	if err := consume(StatusEvent{Connected: true, Snapshot: &snapshot}); err != nil {
		return &eventConsumerFailure{cause: err}
	}
	return nil
}
