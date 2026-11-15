// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package multicluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var inventoryPods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func memberKubeconfig(host string) []byte {
	return []byte(`apiVersion: v1
kind: Config
clusters:
- name: member
  cluster:
    server: ` + host + `
contexts:
- name: member
  context:
    cluster: member
current-context: member
`)
}

func newInventoryManager(t *testing.T, tracker *operations.CoverageTracker, extra ...discovery.InformerOption) (*Manager, *sync.Map) {
	t.Helper()
	clients := &sync.Map{}
	opts := []discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{inventoryPods}),
		// Member identity must take precedence over accidentally shared options.
		discovery.WithClusterID("management")}
	factory := DefaultDiscoveryInformerFactory(append(opts, extra...)...)
	m := New(memory.New(), WithDiscoveryFactory(factory), withDialer(func(config []byte) (dynamic.Interface, error) {
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{inventoryPods: "PodList"})
		clients.Store(string(config), client)
		return client, nil
	}))
	t.Cleanup(m.Stop)
	return m, clients
}

func memberInventory(t *testing.T, tracker *operations.CoverageTracker, name string) *operations.APIInventoryEvidence {
	t.Helper()
	snapshot, err := tracker.SnapshotCoverage(context.Background(), name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.APIInventory
}

// These are real client-go metadata requests to a loopback fixture, not
// Kubernetes object reads. Each member kubeconfig selects its own base path.
func TestManager_MemberInventoryUsesMemberConfigAndIsolatesFailure(t *testing.T) {
	var objectReads atomic.Int32
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var value any
		switch r.URL.Path {
		case "/east/api", "/west/api":
			value = &metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions", APIVersion: "v1"}, Versions: []string{"v1"}}
		case "/east/apis":
			value = &metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
		case "/west/apis":
			w.WriteHeader(http.StatusForbidden)
			value = &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: metav1.StatusReasonForbidden, Code: 403, Message: "synthetic-private-discovery-denial"}
		case "/east/api/v1":
			value = &metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}},
				{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}},
			}}
		default:
			objectReads.Add(1)
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	tracker := operations.NewCoverageTracker(time.Minute)
	var synced atomic.Int32
	m, clients := newInventoryManager(t, tracker, discovery.WithOnSynced(func() { synced.Add(1) }))
	// Stop/join workers before the HTTP fixture closes, even on assertion failure.
	defer m.Stop()
	for _, name := range []string{"east", "west"} {
		if err := m.AddCluster(context.Background(), name, memberKubeconfig(server.URL+"/"+name)); err != nil {
			t.Fatal(err)
		}
	}
	if !waitFor(t, func() bool {
		east, west := memberInventory(t, tracker, "east"), memberInventory(t, tracker, "west")
		return synced.Load() == 2 && east != nil && west != nil && east.State == operations.APIInventoryComplete && west.State == operations.APIInventoryDenied
	}) {
		t.Fatal("member inventory did not record independent complete/denied states")
	}
	east, west := memberInventory(t, tracker, "east"), memberInventory(t, tracker, "west")
	if east.ClusterID != "east" || len(east.Resources) != 2 || east.Stopped || west.ClusterID != "west" || len(west.Resources) != 0 || west.Stopped {
		t.Fatalf("scope or state lost: east=%+v west=%+v", east, west)
	}
	for _, scope := range []string{"", "management", "hidden"} {
		if memberInventory(t, tracker, scope) != nil {
			t.Fatalf("member inventory leaked into %q", scope)
		}
	}
	encoded, _ := json.Marshal(west)
	if strings.Contains(string(encoded), "synthetic-private") {
		t.Fatal("raw discovery error leaked")
	}
	before := calls.Load()
	for range 10 {
		memberInventory(t, tracker, "east")
		memberInventory(t, tracker, "west")
	}
	if calls.Load() != before || objectReads.Load() != 0 {
		t.Fatal("cache reads issued requests or inventory accessed object endpoints")
	}
	clients.Range(func(_, value any) bool {
		for _, action := range value.(*dynamicfake.FakeDynamicClient).Actions() {
			if action.GetResource().Resource != "pods" {
				t.Errorf("inventory expanded watched objects: %v", action)
			}
		}
		return true
	})
	if len(m.Errors()) != 0 {
		t.Fatal("metadata discovery denial stopped an informer")
	}
	generation := east.Generation
	if err := m.RemoveCluster("east"); err != nil {
		t.Fatal(err)
	}
	if !memberInventory(t, tracker, "east").Stopped || memberInventory(t, tracker, "west").Stopped {
		t.Fatal("member removal stopped the wrong inventory")
	}
	if err := m.AddCluster(context.Background(), "east", memberKubeconfig(server.URL+"/east")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, func() bool {
		value := memberInventory(t, tracker, "east")
		return value != nil && value.Generation > generation && value.State == operations.APIInventoryComplete && !value.Stopped && synced.Load() == 3
	}) {
		t.Fatal("reattachment reused old inventory generation")
	}
	m.Stop()
	if !memberInventory(t, tracker, "east").Stopped || !memberInventory(t, tracker, "west").Stopped {
		t.Fatal("shutdown did not stop and join member inventories")
	}
}

type blockingInventoryDiscovery struct {
	kdiscovery.DiscoveryInterface
	started chan context.Context
	stopped chan struct{}
}

func (d *blockingInventoryDiscovery) ServerGroupsWithContext(ctx context.Context) (*metav1.APIGroupList, error) {
	d.started <- ctx
	<-ctx.Done()
	close(d.stopped)
	return nil, ctx.Err()
}
func (*blockingInventoryDiscovery) ServerResourcesForGroupVersionWithContext(context.Context, string) (*metav1.APIResourceList, error) {
	panic("canceled discovery must not continue to resource endpoints")
}

func TestManager_MemberInventoryCancellationJoinsInFlightRequest(t *testing.T) {
	for _, operation := range []string{"remove", "stop", "parent-cancel"} {
		t.Run(operation, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			m, _ := newInventoryManager(t, tracker)
			dc := &blockingInventoryDiscovery{started: make(chan context.Context, 1), stopped: make(chan struct{})}
			m.discoveryDial = func([]byte) (kdiscovery.DiscoveryInterface, error) { return dc, nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := m.AddCluster(ctx, "east", nil); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-dc.started:
				deadline, ok := request.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second {
					t.Fatal("original discovery deadline missing or extended")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("member metadata request did not start")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "remove" {
					if err := m.RemoveCluster("east"); err != nil && !strings.Contains(err.Error(), "failed to sync") {
						t.Error(err)
					}
				} else {
					if operation == "parent-cancel" {
						cancel()
						m.mu.Lock()
						entry := m.clusters["east"]
						m.mu.Unlock()
						<-entry.done
					} else {
						m.Stop()
					}
				}
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("lifecycle did not join metadata request")
			}
			select {
			case <-dc.stopped:
			default:
				t.Fatal("member returned before request stopped")
			}
			evidence := memberInventory(t, tracker, "east")
			if evidence == nil || !evidence.Stopped || !evidence.CheckedAt.IsZero() || evidence.State != operations.APIInventoryUnknown {
				t.Fatal("cancellation manufactured a successful metadata check")
			}
		})
	}
}

func TestManager_MemberDiscoveryConstructionFailureDoesNotAttach(t *testing.T) {
	m, _ := newInventoryManager(t, operations.NewCoverageTracker(time.Minute))
	want := errors.New("synthetic client configuration error")
	m.discoveryDial = func([]byte) (kdiscovery.DiscoveryInterface, error) { return nil, want }
	if err := m.AddCluster(context.Background(), "east", nil); !errors.Is(err, want) {
		t.Fatalf("client error not preserved: %v", err)
	}
	if len(m.ListClusters()) != 0 {
		t.Fatal("failed discovery client left an attached member")
	}
}

type drainingStarter struct{ started, canceled, release chan struct{} }

func (s *drainingStarter) Start(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	close(s.canceled)
	<-s.release
	return ctx.Err()
}
func TestManager_ReservesMemberNameUntilWorkersReturn(t *testing.T) {
	for _, operation := range []string{"remove", "stop"} {
		t.Run(operation, func(t *testing.T) {
			starter := &drainingStarter{make(chan struct{}), make(chan struct{}), make(chan struct{})}
			m := New(memory.New(), WithFactory(func(string, dynamic.Interface, graph.GraphStore) InformerStarter { return starter }), withDialer(func([]byte) (dynamic.Interface, error) { return nil, nil }))
			defer m.Stop()
			if err := m.AddCluster(context.Background(), "east", nil); err != nil {
				t.Fatal(err)
			}
			<-starter.started
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "remove" {
					_ = m.RemoveCluster("east")
				} else {
					m.Stop()
				}
			}()
			<-starter.canceled
			err := m.AddCluster(context.Background(), "east", nil)
			close(starter.release)
			<-done
			if !errors.Is(err, ErrClusterExists) {
				t.Fatalf("same-name member attached before prior worker returned: %v", err)
			}
			if len(m.ListClusters()) != 0 {
				t.Fatal("drained member remains attached")
			}
		})
	}
}

func TestManager_LegacyFactoryDoesNotConstructDiscoveryClient(t *testing.T) {
	m, _ := newTestManager(t)
	defer m.Stop()
	m.discoveryDial = func([]byte) (kdiscovery.DiscoveryInterface, error) {
		t.Error("legacy factory unexpectedly enabled metadata discovery")
		return nil, errors.New("unexpected discovery")
	}
	if err := m.AddCluster(context.Background(), "east", nil); err != nil {
		t.Fatal(err)
	}
}

type failedMemberDiscovery struct{ kdiscovery.DiscoveryInterface }

func (failedMemberDiscovery) ServerGroupsWithContext(context.Context) (*metav1.APIGroupList, error) {
	return nil, errors.New("synthetic-private-provider-failure")
}
func (failedMemberDiscovery) ServerResourcesForGroupVersionWithContext(context.Context, string) (*metav1.APIResourceList, error) {
	panic("failed discovery must not read resources")
}
func TestManager_MemberInventoryFailureAndMissingContextStayQualified(t *testing.T) {
	for _, name := range []string{"failed", "no-context"} {
		t.Run(name, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			m, _ := newInventoryManager(t, tracker)
			m.discoveryDial = func([]byte) (kdiscovery.DiscoveryInterface, error) {
				if name == "failed" {
					return failedMemberDiscovery{}, nil
				}
				return struct{ kdiscovery.DiscoveryInterface }{}, nil
			}
			if err := m.AddCluster(context.Background(), "east", nil); err != nil {
				t.Fatal(err)
			}
			want := operations.APIInventoryFailed
			if name == "no-context" {
				want = operations.APIInventoryUnknown
			}
			if !waitFor(t, func() bool {
				evidence := memberInventory(t, tracker, "east")
				return evidence != nil && evidence.State == want && ((name == "no-context" && evidence.Stopped) || evidence.Revision > 0)
			}) {
				t.Fatal("unsupported or failed provider manufactured complete inventory")
			}
			evidence := memberInventory(t, tracker, "east")
			encoded, _ := json.Marshal(evidence)
			if len(evidence.Resources) != 0 || strings.Contains(string(encoded), "synthetic-private") {
				t.Fatal("failure disclosed descriptors or raw provider error")
			}
			if name == "no-context" && (!evidence.CheckedAt.IsZero() || !evidence.Stopped) {
				t.Fatal("unsupported provider fabricated a completed check")
			}
		})
	}
}
