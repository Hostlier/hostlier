package observation

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Retention limits apply to replaceable telemetry, independently of durable controller operations.
const (
	streamName          = "HOSTLIER_STATUS"
	statusSubject       = "hostlier.observation.status"
	consumerName        = "status_fanout"
	maxSnapshotBytes    = (4 << 20) - 1024
	maxRetainedBytes    = 8 << 20
	maxRetainedMessages = 64
	retentionAge        = 2 * time.Minute
)

// Embedded owns a private JetStream server, one publisher and one acknowledged cache consumer.
// The controller owns the exclusive parent state-directory lock for its entire lifetime.
type Embedded struct {
	server          *server.Server
	connection      *nats.Conn
	js              jetstream.JetStream
	stream          jetstream.Stream
	consumer        jetstream.Consumer
	latest, pending *Hub
	ctx             context.Context
	cancel          context.CancelFunc
	workers         sync.WaitGroup
	closeOnce       sync.Once
	lastSequence    uint64
}

// NewEmbedded starts JetStream without opening any TCP, monitoring, websocket or cluster listener.
func NewEmbedded(parent context.Context, directory string) (*Embedded, error) {
	// Protect telemetry files before the embedded server creates its storage tree.
	if err := privateTelemetryDirectory(directory); err != nil {
		return nil, err
	}
	broker, err := server.NewServer(embeddedOptions(directory))
	// Invalid embedded options must not leave any background service running.
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	b := &Embedded{server: broker, latest: New(), pending: New(), ctx: ctx, cancel: cancel}
	go broker.Start() // NATS owns its internal workers; this startup path is bounded below.
	// Readiness includes JetStream initialization even though TCP accept is disabled.
	if !broker.ReadyForConnections(3 * time.Second) {
		b.Close()
		return nil, errors.New("embedded observation broker failed to start")
	}
	// Partial initialization always closes the private connection and broker before returning.
	if err = b.initialize(); err != nil {
		b.Close()
		return nil, err
	}
	updates, unsubscribe := b.pending.Subscribe()
	b.workers.Add(2)
	go func() { defer b.workers.Done(); defer unsubscribe(); b.publishLoop(updates) }() // One writer owns retries and bounded pending capture.
	go func() { defer b.workers.Done(); b.consumeLoop() }()                             // One reader acknowledges snapshots after updating local fanout.
	return b, nil
}

// embeddedOptions deliberately exposes no network surface and bounds JetStream resource budgets.
func embeddedOptions(directory string) *server.Options {
	return &server.Options{ServerName: "hostlier-observations", DontListen: true, NoSigs: true, NoLog: true, DisableJetStreamBanner: true, JetStream: true, StoreDir: directory, SyncAlways: true, JetStreamMaxMemory: 8 << 20, JetStreamMaxStore: 16 << 20, JetStreamRequestQueueLimit: 128, MaxPayload: maxSnapshotBytes + 1024, MaxPending: 8 << 20, MaxConn: 4}
}

// privateTelemetryDirectory rejects symlink redirection and enforces owner-only traversal.
func privateTelemetryDirectory(directory string) error {
	// A missing path is created without granting other users access to deployment metadata.
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	// Reusing a file or redirected telemetry directory could violate controller state ownership.
	if err != nil {
		return err
	}
	// The protected parent state directory remains owned by the controller's existing writer lock.
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("observation storage must be a private directory")
	}
	return os.Chmod(directory, 0700)
}

// initialize configures one bounded stream and a durable single-message pull consumer.
func (b *Embedded) initialize() error {
	connection, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(b.server), nats.Name("hostlier-observations"), nats.Timeout(time.Second), nats.ReconnectWait(100*time.Millisecond), nats.ReconnectBufSize(maxSnapshotBytes))
	// The placeholder URL is ignored by InProcessServer; no dial to a TCP address occurs.
	if err != nil {
		return err
	}
	b.connection = connection
	b.js, err = jetstream.New(connection)
	// Context-aware JetStream operations keep initialization and shutdown bounded.
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer cancel()
	b.stream, err = b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: streamName, Subjects: []string{statusSubject}, Storage: jetstream.FileStorage, Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, MaxMsgs: maxRetainedMessages, MaxBytes: maxRetainedBytes, MaxAge: retentionAge, MaxMsgSize: maxSnapshotBytes, MaxConsumers: 1, Replicas: 1, Duplicates: retentionAge})
	// A mismatched or unavailable stream cannot silently fall back to unacknowledged delivery.
	if err != nil {
		return err
	}
	return b.initializeConsumer(ctx)
}

// initializeConsumer recovers retained state before resuming idempotent acknowledged fanout.
func (b *Embedded) initializeConsumer(ctx context.Context) error {
	last, err := b.stream.GetLastMsgForSubject(ctx, statusSubject)
	// Retained state can warm the bus; controller epochs prevent treating old-process state as fresh.
	if err == nil {
		b.acceptSnapshot(last.Sequence, string(last.Data))
	} else if !errors.Is(err, jetstream.ErrMsgNotFound) { // Only a genuinely empty retained stream is a normal cold start.
		return err
	}
	b.consumer, err = b.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{Durable: consumerName, FilterSubject: statusSubject, DeliverPolicy: jetstream.DeliverLastPolicy, AckPolicy: jetstream.AckExplicitPolicy, AckWait: 2 * time.Second, MaxAckPending: 1, MaxWaiting: 1, MaxRequestBatch: 1, MaxRequestExpires: 2 * time.Second, ReplayPolicy: jetstream.ReplayInstantPolicy})
	return err
}

// Latest never performs broker I/O in a CLI, browser or controller request path.
func (b *Embedded) Latest() string { return b.latest.Latest() }

// Subscribe shares one JetStream consumer across all bounded controller stream subscribers.
func (b *Embedded) Subscribe() (<-chan struct{}, func()) { return b.latest.Subscribe() }

// Close interrupts requests, waits for owned workers and flushes JetStream before returning.
func (b *Embedded) Close() {
	// Controller shutdown and initialization failure may both attempt cleanup safely.
	b.closeOnce.Do(func() {
		b.cancel()
		// Closing the private connection wakes pending pull requests immediately.
		if b.connection != nil {
			b.connection.Close()
		}
		b.workers.Wait()
		b.server.Shutdown()
		b.server.WaitForShutdown()
	})
}
