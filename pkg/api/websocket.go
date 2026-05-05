package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
)

// Wire-protocol message types. Keep these stable across versions —
// clients (UI, third-party tooling) match on the literal string.
type MessageType string

const (
	MsgTypeSubscribe   MessageType = "SUBSCRIBE"
	MsgTypeUnsubscribe MessageType = "UNSUBSCRIBE"
	MsgTypeGraphUpdate MessageType = "GRAPH_UPDATE"
	MsgTypePing        MessageType = "PING"
	MsgTypePong        MessageType = "PONG"
)

// Envelope is the single JSON shape exchanged in both directions over
// the WebSocket. The Type field discriminates; the rest are
// type-specific and use omitempty.
type Envelope struct {
	Type      MessageType      `json:"type"`
	Level     aggregator.Level `json:"level,omitempty"`
	Namespace string           `json:"namespace,omitempty"`
	Kind      string           `json:"kind,omitempty"`
	Name      string           `json:"name,omitempty"`
	Patch     json.RawMessage  `json:"patch,omitempty"`
	Revision  uint64           `json:"revision,omitempty"`
}

// SubscriptionFilter is what the server records per connection so it
// can decide whether a given GraphUpdate matches.
type SubscriptionFilter struct {
	Level     aggregator.Level
	Namespace string
	Kind      string
	Name      string
}

// GraphUpdate is the in-process representation that callers (the
// informer, in P1-T16) push into the hub. The hub serialises it into
// an Envelope before sending.
type GraphUpdate struct {
	Level     aggregator.Level
	Namespace string
	Kind      string
	Name      string
	Patch     json.RawMessage
	Revision  uint64
}

// Defaults. The heartbeat period is overridable per-hub for tests
// that want to assert ping behaviour without waiting half a minute.
const (
	defaultSendBuffer     = 1024
	defaultHeartbeatEvery = 30 * time.Second
	writeTimeout          = 5 * time.Second
)

// HubOption configures a WatchHub.
type HubOption func(*WatchHub)

// WithHeartbeatPeriod overrides the default 30s server-side ping
// interval. Used by tests; production callers leave it at the default.
func WithHeartbeatPeriod(d time.Duration) HubOption {
	return func(h *WatchHub) { h.heartbeat = d }
}

// WithSendBuffer overrides the per-subscription outbound buffer size.
// Larger buffers tolerate slow clients longer at the cost of memory.
func WithSendBuffer(n int) HubOption {
	return func(h *WatchHub) { h.sendBuffer = n }
}

// WatchHub holds the live WebSocket subscriptions and broadcasts
// GraphUpdate events to those whose filter matches. The hub does not
// know about the informer; pkg/discovery wires events in via a call
// to Broadcast (P1-T16).
type WatchHub struct {
	mu     sync.RWMutex
	subs   map[string]*subscription
	nextID atomic.Uint64

	heartbeat  time.Duration
	sendBuffer int
}

// NewWatchHub builds a hub with the given options.
func NewWatchHub(opts ...HubOption) *WatchHub {
	h := &WatchHub{
		subs:       make(map[string]*subscription),
		heartbeat:  defaultHeartbeatEvery,
		sendBuffer: defaultSendBuffer,
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// subscription is one live WebSocket connection plus its filter and
// outbound queue. send is bounded; if the queue is full when an update
// arrives the update is dropped for this subscriber (a slow client
// must not block broadcast to everyone else).
type subscription struct {
	id     string
	filter SubscriptionFilter
	send   chan []byte
}

// Handle is the http.HandlerFunc for GET /api/v1alpha1/watch. It
// upgrades the connection, reads exactly one SUBSCRIBE envelope, then
// runs reader+writer goroutines until the client disconnects or the
// request context cancels.
func (h *WatchHub) Handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// CORS is owned by the corsMiddleware chain; for the WS handshake
		// we accept any origin and rely on the deployment's Ingress for
		// real isolation.
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Warn("ws accept failed", "err", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx := r.Context()

	// First message must be SUBSCRIBE.
	var first Envelope
	if err := wsjson.Read(ctx, conn, &first); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "missing SUBSCRIBE")
		return
	}
	if first.Type != MsgTypeSubscribe {
		_ = conn.Close(websocket.StatusPolicyViolation, "first message must be SUBSCRIBE")
		return
	}

	sub := &subscription{
		id: subID(h.nextID.Add(1)),
		filter: SubscriptionFilter{
			Level:     first.Level,
			Namespace: first.Namespace,
			Kind:      first.Kind,
			Name:      first.Name,
		},
		send: make(chan []byte, h.sendBuffer),
	}
	h.register(sub)
	defer h.unregister(sub.id)

	// Writer goroutine: drains sub.send to the connection and emits a
	// PING every heartbeat tick.
	writerCtx, cancelWriter := context.WithCancel(ctx)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		ticker := time.NewTicker(h.heartbeat)
		defer ticker.Stop()
		ping, _ := json.Marshal(Envelope{Type: MsgTypePing})
		for {
			select {
			case <-writerCtx.Done():
				return
			case msg, ok := <-sub.send:
				if !ok {
					return
				}
				if err := writeWithTimeout(writerCtx, conn, msg); err != nil {
					return
				}
			case <-ticker.C:
				if err := writeWithTimeout(writerCtx, conn, ping); err != nil {
					return
				}
			}
		}
	}()

	// Reader loop: handles inbound PING (echo PONG), PONG (no-op), and
	// UNSUBSCRIBE (terminate this connection).
	pongPayload, _ := json.Marshal(Envelope{Type: MsgTypePong})
	for {
		var msg Envelope
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			break
		}
		switch msg.Type {
		case MsgTypePing:
			// Best-effort echo; if the buffer is full we just drop it.
			select {
			case sub.send <- pongPayload:
			default:
			}
		case MsgTypePong:
			// Client acknowledging our heartbeat. Nothing to do.
		case MsgTypeSubscribe:
			// Re-SUBSCRIBE on the same connection narrows or broadens
			// the filter without forcing a reconnect. Used by
			// ResourcePage to focus on a single resource's events on
			// mount and reset to cluster on unmount.
			h.updateFilter(sub.id, SubscriptionFilter{
				Level:     msg.Level,
				Namespace: msg.Namespace,
				Kind:      msg.Kind,
				Name:      msg.Name,
			})
		case MsgTypeUnsubscribe:
			cancelWriter()
			<-writerDone
			return
		default:
			// Unknown messages are silently ignored so future protocol
			// extensions don't break older servers.
		}
	}
	cancelWriter()
	<-writerDone
}

// Broadcast pushes update to every subscription whose filter matches.
// Slow subscribers (full send buffer) drop the update rather than
// blocking the broadcaster.
func (h *WatchHub) Broadcast(update GraphUpdate) {
	payload, err := json.Marshal(Envelope{
		Type:      MsgTypeGraphUpdate,
		Level:     update.Level,
		Namespace: update.Namespace,
		Kind:      update.Kind,
		Name:      update.Name,
		Patch:     update.Patch,
		Revision:  update.Revision,
	})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, sub := range h.subs {
		if !filterMatches(sub.filter, update) {
			continue
		}
		select {
		case sub.send <- payload:
		default:
			slog.Warn("dropping update for slow subscriber",
				"sub", sub.id,
				"buffer_capacity", cap(sub.send),
			)
		}
	}
}

// BroadcastEvent is a thin convenience over Broadcast that fits the
// pkg/discovery.Broadcaster signature. Translates an informer event
// into a cluster-level GraphUpdate carrying the affected resource's
// triple. The cluster Level intentionally fans out to every
// subscriber — filterMatches narrows by namespace/kind/name for
// workload + resource subscribers.
func (h *WatchHub) BroadcastEvent(_op, namespace, kind, name string) {
	h.Broadcast(GraphUpdate{
		Level:     aggregator.LevelCluster,
		Namespace: namespace,
		Kind:      kind,
		Name:      name,
		Revision:  h.nextID.Add(1),
	})
}

// SubscriberCount returns the number of live subscriptions; useful for
// tests and for /metrics later in P1-T6.
func (h *WatchHub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

func (h *WatchHub) register(s *subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[s.id] = s
}

func (h *WatchHub) unregister(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.subs[id]; ok {
		close(s.send)
		delete(h.subs, id)
	}
}

// updateFilter swaps the filter on a live subscription so a re-SUBSCRIBE
// from the same client narrows or broadens what events it sees without
// reconnecting. Missing ids are a no-op (the connection may have closed
// between the read and the write).
func (h *WatchHub) updateFilter(id string, f SubscriptionFilter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.subs[id]; ok {
		s.filter = f
	}
}

// filterMatches reports whether update is interesting to a given
// subscription filter.
//
//	cluster   - everything matches
//	namespace - matches when filter.Namespace == update.Namespace
//	            (an empty update.Namespace counts as cluster-scoped and
//	            is delivered to every namespace subscriber so they see
//	            cross-cutting changes)
//	workload  - matches on the (Namespace, Kind, Name) triple
//	resource  - matches on the (Namespace, Kind, Name) triple
func filterMatches(f SubscriptionFilter, u GraphUpdate) bool {
	switch f.Level {
	case aggregator.LevelCluster:
		return true
	case aggregator.LevelNamespace:
		return f.Namespace == u.Namespace || u.Namespace == ""
	case aggregator.LevelWorkload, aggregator.LevelResource:
		return f.Namespace == u.Namespace && f.Kind == u.Kind && f.Name == u.Name
	}
	return false
}

func writeWithTimeout(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := conn.Write(wctx, websocket.MessageText, payload); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// subID renders a stable per-hub subscription id.
func subID(n uint64) string {
	const digits = "0123456789"
	if n == 0 {
		return "sub-0"
	}
	var rev []byte
	for n > 0 {
		rev = append(rev, digits[n%10])
		n /= 10
	}
	out := make([]byte, 0, 4+len(rev))
	out = append(out, "sub-"...)
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return string(out)
}
