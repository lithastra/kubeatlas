package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// seedAndServe builds a populated store, binds a free TCP port,
// constructs an api.Server on it, runs Start in a goroutine, and
// returns base URL + server + cleanup. Tests exercise the production
// path so the middleware chain + routing are part of the contract
// under test.
func seedAndServe(t *testing.T, seed func(s graph.GraphStore)) (string, *api.Server, func()) {
	t.Helper()

	store := memory.New()
	if seed != nil {
		seed(store)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	srv := api.New(addr, store, aggregator.NewRegistry())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cleanup := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Server.Start returned %v, want context.Canceled or nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Server.Start did not return within 5s")
		}
	}
	return "http://" + addr, srv, cleanup
}

// petClinicSeed is the canonical small fixture used by handler tests.
func petClinicSeed(s graph.GraphStore) {
	ctx := context.Background()
	dep := graph.Resource{Kind: "Deployment", Namespace: "petclinic", Name: "api",
		UID: types.UID("dep-uid"), Labels: map[string]string{"app": "api"}}
	rs := graph.Resource{Kind: "ReplicaSet", Namespace: "petclinic", Name: "api-rs",
		UID:             types.UID("rs-uid"),
		OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: types.UID("dep-uid")}}}
	pod := graph.Resource{Kind: "Pod", Namespace: "petclinic", Name: "api-1",
		UID:             types.UID("pod-uid"),
		OwnerReferences: []graph.OwnerRef{{Kind: "ReplicaSet", Name: "api-rs", UID: types.UID("rs-uid")}}}
	cm := graph.Resource{Kind: "ConfigMap", Namespace: "petclinic", Name: "app-config"}
	for _, r := range []graph.Resource{dep, rs, pod, cm} {
		_ = s.UpsertResource(ctx, r)
	}
	for _, e := range []graph.Edge{
		{From: pod.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns},
		{From: rs.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns},
		{From: dep.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap},
	} {
		_ = s.UpsertEdge(ctx, e)
	}
}

func getJSON(t *testing.T, url string, into any) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if into != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(body, into); err != nil {
			t.Fatalf("unmarshal %s: %v\nbody=%s", url, err, body)
		}
	}
	return resp, body
}

// --- /healthz already covered in server_test.go; tests below cover
// the new endpoints introduced in P1-T6.

func TestHandleReadyz_NotReadyReturns503(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()
	resp, _ := getJSON(t, base+"/readyz", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status before ready = %d, want 503", resp.StatusCode)
	}
}

func TestHandleReadyz_ReadyReturns200(t *testing.T) {
	base, srv, stop := seedAndServe(t, nil)
	defer stop()
	srv.Readiness().MarkReady()
	resp, _ := getJSON(t, base+"/readyz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status after MarkReady = %d, want 200", resp.StatusCode)
	}
}

func TestHandleGraph_LevelClusterReturnsView(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()

	var view aggregator.View
	resp, body := getJSON(t, base+"/api/v1alpha1/graph?level=cluster", &view)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if view.Level != aggregator.LevelCluster {
		t.Errorf("Level = %q, want cluster", view.Level)
	}
	if len(view.Nodes) == 0 {
		t.Error("expected at least one namespace node")
	}
}

func TestHandleGraph_LevelWorkloadRequiresKindAndName(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()

	resp, _ := getJSON(t, base+"/api/v1alpha1/graph?level=workload&namespace=petclinic", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing kind/name should be 400, got %d", resp.StatusCode)
	}
}

func TestHandleGraph_LevelWorkloadHappyPath(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()

	var view aggregator.View
	resp, body := getJSON(t,
		base+"/api/v1alpha1/graph?level=workload&namespace=petclinic&kind=Deployment&name=api",
		&view)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if view.Level != aggregator.LevelWorkload {
		t.Errorf("Level = %q, want workload", view.Level)
	}
}

func TestHandleGraph_UnknownLevelReturns400(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()
	resp, body := getJSON(t, base+"/api/v1alpha1/graph?level=banana", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown level should be 400, got %d (body=%s)", resp.StatusCode, body)
	}
}

func TestHandleGraph_MissingLevelReturns400(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1alpha1/graph", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing level should be 400, got %d", resp.StatusCode)
	}
}

func TestHandleResource_ReturnsDetailWithEdges(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()

	var detail api.ResourceDetailResponse
	resp, body := getJSON(t, base+"/api/v1alpha1/resources/petclinic/Deployment/api", &detail)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if detail.Resource.Kind != "Deployment" || detail.Resource.Name != "api" {
		t.Errorf("wrong resource: %+v", detail.Resource)
	}
	// Deployment has 1 outgoing edge (to ConfigMap) and 1 incoming
	// (from ReplicaSet via OWNS) per the seed.
	if len(detail.Outgoing) != 1 {
		t.Errorf("Outgoing = %d, want 1: %v", len(detail.Outgoing), detail.Outgoing)
	}
	if len(detail.Incoming) != 1 {
		t.Errorf("Incoming = %d, want 1: %v", len(detail.Incoming), detail.Incoming)
	}
}

func TestHandleResource_MissingReturns404(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()
	resp, body := getJSON(t, base+"/api/v1alpha1/resources/petclinic/Deployment/ghost", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body=%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"code":"not_found"`) {
		t.Errorf("body missing code=not_found: %s", body)
	}
}

func TestHandleIncomingOutgoing_HappyPath(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()

	var in []graph.Edge
	resp, _ := getJSON(t, base+"/api/v1alpha1/resources/petclinic/Deployment/api/incoming", &in)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("incoming status = %d, want 200", resp.StatusCode)
	}
	if len(in) != 1 || in[0].From != "petclinic/ReplicaSet/api-rs" {
		t.Errorf("unexpected incoming: %v", in)
	}

	var out []graph.Edge
	resp, _ = getJSON(t, base+"/api/v1alpha1/resources/petclinic/Deployment/api/outgoing", &out)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("outgoing status = %d, want 200", resp.StatusCode)
	}
	if len(out) != 1 || out[0].To != "petclinic/ConfigMap/app-config" {
		t.Errorf("unexpected outgoing: %v", out)
	}
}

func TestHandleSearch_FindsByNameSubstring(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()
	var resp api.SearchResponse
	httpResp, body := getJSON(t, base+"/api/v1alpha1/search?q=api", &resp)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", httpResp.StatusCode, body)
	}
	if resp.Total < 1 {
		t.Errorf("expected at least one match for 'api', got %d", resp.Total)
	}
}

func TestHandleSearch_EmptyQueryReturns400(t *testing.T) {
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1alpha1/search", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleSearch_LimitCappedAtMax(t *testing.T) {
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		// Generate 250 matchable resources.
		for i := 0; i < 250; i++ {
			_ = s.UpsertResource(ctx, graph.Resource{
				Kind: "Pod", Namespace: "demo", Name: podLabel(i),
			})
		}
	})
	defer stop()
	var resp api.SearchResponse
	httpResp, _ := getJSON(t, base+"/api/v1alpha1/search?q=p&limit=500", &resp)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", httpResp.StatusCode)
	}
	// Limit cap = 200.
	if len(resp.Matches) != 200 {
		t.Errorf("len(matches) = %d, want 200 (cap)", len(resp.Matches))
	}
	if !resp.Truncated {
		t.Error("expected Truncated=true when total > matches")
	}
}

func TestHandleMetrics_PromExposition(t *testing.T) {
	base, srv, stop := seedAndServe(t, petClinicSeed)
	defer stop()
	srv.Readiness().MarkReady()

	// Drive a request so the counter has something non-zero.
	_, _ = getJSON(t, base+"/api/v1alpha1/graph?level=cluster", nil)

	resp, body := getJSON(t, base+"/metrics", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	text := string(body)
	for _, want := range []string{
		"# TYPE kubeatlas_goroutines gauge",
		"kubeatlas_goroutines ",
		"kubeatlas_informer_synced 1",
		"# TYPE kubeatlas_api_requests_total counter",
		`kubeatlas_api_requests_total{method="GET",status="200"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", want, text)
		}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain*", ct)
	}
}

func TestHandleResource_ClusterScopedSentinel(t *testing.T) {
	// Cluster-scoped resources have no namespace; the URL uses "_" as
	// the placeholder so paths stay parseable.
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		_ = s.UpsertResource(context.Background(), graph.Resource{
			Kind: "Namespace", Name: "petclinic",
		})
	})
	defer stop()
	var detail api.ResourceDetailResponse
	resp, body := getJSON(t, base+"/api/v1alpha1/resources/_/Namespace/petclinic", &detail)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if detail.Resource.Namespace != "" {
		t.Errorf("Namespace = %q, want empty", detail.Resource.Namespace)
	}
}

// --- httptest sidecar for endpoints that don't need a real listener ---

func TestErrorWiring_NotFoundJSONShape(t *testing.T) {
	// Use httptest.NewRecorder against the same server's handler so we
	// don't need a real socket.
	store := memory.New()
	srv := api.New(":0", store, aggregator.NewRegistry())

	// The handler chain is built inside Start; for this test we hit the
	// endpoint via the live server path (consistent with the rest).
	base, _, stop := seedAndServe(t, petClinicSeed)
	defer stop()

	resp, body := getJSON(t, base+"/api/v1alpha1/resources/petclinic/Deployment/missing", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var er api.ErrorResponse
	if err := json.Unmarshal(body, &er); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if er.Code != api.CodeNotFound {
		t.Errorf("Code = %q, want not_found", er.Code)
	}
	if er.Error == "" {
		t.Error("Error message empty")
	}
	_ = srv
}

// --- helpers (these mirror small utilities the existing test files
// already use; kept tiny to avoid a circular dependency between
// _test.go files in the same package).

func podLabel(i int) string {
	const digits = "0123456789"
	buf := []byte("p-")
	if i == 0 {
		return string(append(buf, '0'))
	}
	var rev []byte
	for i > 0 {
		rev = append(rev, digits[i%10])
		i /= 10
	}
	for j := len(rev) - 1; j >= 0; j-- {
		buf = append(buf, rev[j])
	}
	return string(buf)
}
