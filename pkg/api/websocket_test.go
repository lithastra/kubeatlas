package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
)

// startHubServer spins up an httptest server that registers a single
// WatchHub on /watch (a shorter path than the production one because
// we don't need the v1alpha1 prefix in tests).
func startHubServer(t *testing.T, opts ...api.HubOption) (*api.WatchHub, string, func()) {
	t.Helper()
	hub := api.NewWatchHub(opts...)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /watch", hub.Handle)
	srv := httptest.NewServer(mux)
	wsURL := "ws" + srv.URL[len("http"):] + "/watch"
	return hub, wsURL, srv.Close
}

func dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func subscribe(t *testing.T, conn *websocket.Conn, filter api.SubscriptionFilter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	env := api.Envelope{
		Type:      api.MsgTypeSubscribe,
		Level:     filter.Level,
		Namespace: filter.Namespace,
		Kind:      filter.Kind,
		Name:      filter.Name,
	}
	if err := wsjson.Write(ctx, conn, env); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}
}

func waitForSubscriberCount(t *testing.T, hub *api.WatchHub, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.SubscriberCount() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscriber count = %d, want %d (timeout)", hub.SubscriberCount(), want)
}

func TestWatchHub_SubscribeAndReceiveBroadcast(t *testing.T) {
	hub, url, stop := startHubServer(t)
	defer stop()

	conn := dial(t, url)
	defer conn.CloseNow()
	subscribe(t, conn, api.SubscriptionFilter{Level: aggregator.LevelCluster})
	waitForSubscriberCount(t, hub, 1)

	hub.Broadcast(api.GraphUpdate{
		Level:    aggregator.LevelCluster,
		Patch:    json.RawMessage(`{"added":1}`),
		Revision: 42,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got api.Envelope
	if err := wsjson.Read(ctx, conn, &got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Type != api.MsgTypeGraphUpdate {
		t.Errorf("Type = %q, want GRAPH_UPDATE", got.Type)
	}
	if got.Revision != 42 {
		t.Errorf("Revision = %d, want 42", got.Revision)
	}
}

func TestWatchHub_FirstMessageMustBeSubscribe(t *testing.T) {
	_, url, stop := startHubServer(t)
	defer stop()

	conn := dial(t, url)
	// Send a PING as the first message — server should reject + close.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, api.Envelope{Type: api.MsgTypePing}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Reading anything else should fail because the server closed.
	rctx, rcancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer rcancel()
	var msg api.Envelope
	if err := wsjson.Read(rctx, conn, &msg); err == nil {
		t.Errorf("expected read error after policy-violation close, got msg=%+v", msg)
	}
}

func TestWatchHub_FilterByNamespaceDoesNotDeliverOtherNamespaces(t *testing.T) {
	hub, url, stop := startHubServer(t)
	defer stop()

	conn := dial(t, url)
	defer conn.CloseNow()
	subscribe(t, conn, api.SubscriptionFilter{
		Level:     aggregator.LevelNamespace,
		Namespace: "team-a",
	})
	waitForSubscriberCount(t, hub, 1)

	// Broadcast a non-matching update first, then a matching one. The
	// reader's first message must be the matching one — if the
	// non-matching update somehow bypassed the filter, it would arrive
	// first and fail the assertion. (Read timeouts close coder/websocket
	// connections, so we use ordering rather than "wait for absence".)
	hub.Broadcast(api.GraphUpdate{Level: aggregator.LevelNamespace, Namespace: "team-b", Revision: 99})
	hub.Broadcast(api.GraphUpdate{Level: aggregator.LevelNamespace, Namespace: "team-a", Revision: 7})

	rctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	var got api.Envelope
	if err := wsjson.Read(rctx, conn, &got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Namespace != "team-a" || got.Revision != 7 {
		t.Errorf("first delivered update = %+v, want team-a/rev=7 (team-b leaked through filter)", got)
	}
}

func TestWatchHub_HeartbeatPing(t *testing.T) {
	hub, url, stop := startHubServer(t, api.WithHeartbeatPeriod(50*time.Millisecond))
	defer stop()

	conn := dial(t, url)
	defer conn.CloseNow()
	subscribe(t, conn, api.SubscriptionFilter{Level: aggregator.LevelCluster})
	waitForSubscriberCount(t, hub, 1)

	rctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	var got api.Envelope
	if err := wsjson.Read(rctx, conn, &got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Type != api.MsgTypePing {
		t.Errorf("Type = %q, want PING", got.Type)
	}
}

func TestWatchHub_ReSubscribeNarrowsFilter(t *testing.T) {
	hub, url, stop := startHubServer(t)
	defer stop()

	conn := dial(t, url)
	defer conn.CloseNow()
	subscribe(t, conn, api.SubscriptionFilter{Level: aggregator.LevelCluster})
	waitForSubscriberCount(t, hub, 1)

	// Re-SUBSCRIBE to a workload scope — broadcasts that don't match
	// the (Namespace, Kind, Name) triple must stop arriving.
	subscribe(t, conn, api.SubscriptionFilter{
		Level:     aggregator.LevelWorkload,
		Namespace: "demo",
		Kind:      "Deployment",
		Name:      "api",
	})
	// Server processes the re-SUBSCRIBE asynchronously in its read
	// loop; give it a moment to update the filter before broadcasting,
	// otherwise the test races the in-flight server-side write.
	time.Sleep(50 * time.Millisecond)
	// Send back-to-back updates: first non-matching (filtered now),
	// then matching. Read once and assert we receive the matching one
	// — if the filter hadn't been narrowed, the non-matching update
	// would arrive first.
	hub.Broadcast(api.GraphUpdate{
		Level: aggregator.LevelWorkload, Namespace: "demo", Kind: "Service", Name: "api", Revision: 99,
	})
	hub.Broadcast(api.GraphUpdate{
		Level: aggregator.LevelWorkload, Namespace: "demo", Kind: "Deployment", Name: "api", Revision: 7,
	})

	// First Broadcast lost a race with the in-flight re-SUBSCRIBE write
	// in some runs; the easiest deterministic check is to wait briefly
	// then broadcast both messages.
	rctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	var got api.Envelope
	if err := wsjson.Read(rctx, conn, &got); err != nil {
		t.Fatalf("read after re-SUBSCRIBE: %v", err)
	}
	if got.Kind != "Deployment" || got.Revision != 7 {
		t.Errorf("first delivered after narrow = %+v, want Deployment/rev=7 (Service/rev=99 leaked)", got)
	}
}

func TestWatchHub_UnsubscribeReleasesSlot(t *testing.T) {
	hub, url, stop := startHubServer(t)
	defer stop()

	conn := dial(t, url)
	subscribe(t, conn, api.SubscriptionFilter{Level: aggregator.LevelCluster})
	waitForSubscriberCount(t, hub, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, api.Envelope{Type: api.MsgTypeUnsubscribe}); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.CloseNow()
	waitForSubscriberCount(t, hub, 0)
}

func TestWatchHub_SlowSubscriberDoesNotBlockBroadcast(t *testing.T) {
	// Tiny send buffer makes it trivially overflowable.
	hub, url, stop := startHubServer(t, api.WithSendBuffer(1))
	defer stop()

	conn := dial(t, url)
	defer conn.CloseNow()
	subscribe(t, conn, api.SubscriptionFilter{Level: aggregator.LevelCluster})
	waitForSubscriberCount(t, hub, 1)

	// Don't read; broadcast 100 updates. Should not block more than a
	// few hundred milliseconds even with a 1-deep buffer.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			hub.Broadcast(api.GraphUpdate{Level: aggregator.LevelCluster, Revision: uint64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on slow subscriber")
	}
}

func TestWatchHub_ConcurrentSubscribers(t *testing.T) {
	// 1000 concurrent subscribers + one broadcast; verify the goroutine
	// count returns to baseline (±20) after everyone disconnects.
	hub, url, stop := startHubServer(t)
	defer stop()

	const n = 1000
	baseline := runtime.NumGoroutine()

	conns := make([]*websocket.Conn, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			conn := dial(t, url)
			conns[i] = conn
			subscribe(t, conn, api.SubscriptionFilter{Level: aggregator.LevelCluster})
		}(i)
	}
	wg.Wait()

	waitForSubscriberCount(t, hub, n)

	// Single broadcast — every subscriber should be able to read at
	// least once. Read with a generous timeout per connection.
	hub.Broadcast(api.GraphUpdate{Level: aggregator.LevelCluster, Revision: 1})
	var receiveWG sync.WaitGroup
	receiveWG.Add(n)
	received := make([]bool, n)
	for i, c := range conns {
		go func(i int, c *websocket.Conn) {
			defer receiveWG.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var env api.Envelope
			if err := wsjson.Read(ctx, c, &env); err == nil && env.Type == api.MsgTypeGraphUpdate {
				received[i] = true
			}
		}(i, c)
	}
	receiveWG.Wait()

	got := 0
	for _, ok := range received {
		if ok {
			got++
		}
	}
	if got < n {
		t.Errorf("only %d/%d subscribers received the broadcast", got, n)
	}

	for _, c := range conns {
		_ = c.CloseNow()
	}

	// Allow goroutines to wind down.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.SubscriberCount() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hub.SubscriberCount() != 0 {
		t.Errorf("after disconnects, %d subscriptions still registered", hub.SubscriberCount())
	}
	// Goroutine count is fuzzy under -race; allow ±50 from baseline.
	if delta := runtime.NumGoroutine() - baseline; delta > 50 {
		t.Errorf("goroutine count grew by %d (baseline %d) — possible leak", delta, baseline)
	}
}

// Quick sanity that the server's WS route is wired by registerRoutes.
func TestServer_WatchEndpointReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := api.New(addr, nil, aggregator.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	// Wait for listener.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	wsURL := "ws://" + addr + "/api/v1alpha1/watch"
	conn := dial(t, wsURL)
	defer conn.CloseNow()
	subscribe(t, conn, api.SubscriptionFilter{Level: aggregator.LevelCluster})
	waitForSubscriberCount(t, srv.Hub(), 1)
}
