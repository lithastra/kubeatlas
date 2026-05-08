// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// startEnvOrSkip mirrors pkg/discovery/informer_test.go's helper:
// boot envtest, or skip the test cleanly when the kube-apiserver +
// etcd binaries are missing on this dev/CI box.
func startEnvOrSkip(t *testing.T) (*envtest.Environment, dynamic.Interface, apiextensionsclient.Interface) {
	t.Helper()
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "kube-apiserver") || strings.Contains(msg, "etcd") || strings.Contains(msg, "control plane") || strings.Contains(msg, "binary") || strings.Contains(msg, "no such file") {
			t.Skipf("envtest binaries not available; install with `setup-envtest use 1.30.x`. Underlying error: %v", err)
		}
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	apiext, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return env, dyn, apiext
}

// recordingRego captures every EvaluateForResource call so the test
// can verify the CRD pipeline routed the right resource through.
type recordingRego struct {
	mu      sync.Mutex
	calls   []graph.Resource
	returns []graph.Edge
}

func (r *recordingRego) EvaluateForResource(_ context.Context, res graph.Resource) ([]graph.Edge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, res)
	out := make([]graph.Edge, len(r.returns))
	copy(out, r.returns)
	return out, nil
}

func (r *recordingRego) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingRego) callsForKind(kind string) []graph.Resource {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []graph.Resource
	for _, c := range r.calls {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// fooCRD is the minimal CRD definition used across the tests:
// example.com/v1 Foo, namespaced, no schema validation (open object).
func fooCRD() *apiextensionsv1.CustomResourceDefinition {
	preserveUnknown := true
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "foos.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "foos",
				Singular: "foo",
				Kind:     "Foo",
				ListKind: "FooList",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type:                   "object",
							XPreserveUnknownFields: &preserveUnknown,
						},
					},
				},
			},
		},
	}
}

// waitFor polls cond every 50ms until it returns true or the
// timeout fires. Test failures call t.Fatalf with the supplied msg.
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waitFor: timed out after %s — %s", timeout, msg)
}

// TestDiscovery_StartsEmpty: with no CRDs in the cluster, Discovery
// boots cleanly and reports zero registered GVRs after cache sync.
func TestDiscovery_StartsEmpty(t *testing.T) {
	_, dyn, _ := startEnvOrSkip(t)

	store := memory.New()
	d := New(dyn, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Start(ctx) }()

	// Give the meta informer a beat to sync, then assert nothing
	// was registered (envtest comes with zero CRDs by default).
	time.Sleep(500 * time.Millisecond)
	if got := len(d.RegisteredGVRs()); got != 0 {
		t.Errorf("RegisteredGVRs len = %d, want 0", got)
	}
}

// TestDiscovery_PicksUpRuntimeCRD: create a CRD after Discovery is
// running, verify the per-CRD informer registers, instantiate one
// CR, verify Rego gets called and the resource lands in the store.
func TestDiscovery_PicksUpRuntimeCRD(t *testing.T) {
	_, dyn, apiext := startEnvOrSkip(t)

	store := memory.New()
	rec := &recordingRego{}
	d := New(dyn, store, WithRegoEvaluator(rec))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Start(ctx) }()

	// Step 1: create the CRD via the typed apiextensions client.
	if _, err := apiext.ApiextensionsV1().CustomResourceDefinitions().
		Create(context.Background(), fooCRD(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD: %v", err)
	}

	// Step 2: wait for Discovery to register the Foo GVR.
	wantGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "foos"}
	waitFor(t, 10*time.Second, "Foo GVR registered", func() bool {
		for _, g := range d.RegisteredGVRs() {
			if g == wantGVR {
				return true
			}
		}
		return false
	})

	// Step 3: create a Foo instance via the dynamic client.
	const ns = "default"
	foo := &unstructured.Unstructured{}
	foo.SetAPIVersion("example.com/v1")
	foo.SetKind("Foo")
	foo.SetName("demo")
	foo.SetNamespace(ns)
	if _, err := dyn.Resource(wantGVR).Namespace(ns).
		Create(context.Background(), foo, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Foo: %v", err)
	}

	// Step 4: verify the resource flowed into the store and Rego.
	waitFor(t, 10*time.Second, "Foo arrived in store", func() bool {
		_, err := store.GetResource(context.Background(), "default/Foo/demo")
		return err == nil
	})
	waitFor(t, 5*time.Second, "Rego saw the Foo", func() bool {
		return len(rec.callsForKind("Foo")) > 0
	})
}

// TestDiscovery_DeregistersOnCRDDelete: deleting the CRD must remove
// the per-CRD informer from RegisteredGVRs (in-store data is left
// alone — the resource may still be useful for diagnostics).
func TestDiscovery_DeregistersOnCRDDelete(t *testing.T) {
	_, dyn, apiext := startEnvOrSkip(t)

	store := memory.New()
	d := New(dyn, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Start(ctx) }()

	if _, err := apiext.ApiextensionsV1().CustomResourceDefinitions().
		Create(context.Background(), fooCRD(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD: %v", err)
	}

	wantGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "foos"}
	waitFor(t, 10*time.Second, "Foo GVR registered", func() bool {
		for _, g := range d.RegisteredGVRs() {
			if g == wantGVR {
				return true
			}
		}
		return false
	})

	if err := apiext.ApiextensionsV1().CustomResourceDefinitions().
		Delete(context.Background(), "foos.example.com", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete CRD: %v", err)
	}

	waitFor(t, 10*time.Second, "Foo GVR unregistered", func() bool {
		for _, g := range d.RegisteredGVRs() {
			if g == wantGVR {
				return false
			}
		}
		return true
	})
}

// TestNew_RejectsNilDeps: the factory does not start until Start is
// called, so dependency validation surfaces there.
func TestNew_RejectsNilDeps(t *testing.T) {
	store := memory.New()
	d := New(nil, store)
	err := d.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "dynamic client") {
		t.Errorf("Start(nil dyn): err = %v, want dynamic-client error", err)
	}

	d = New(&fakeDynamic{}, nil)
	err = d.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "store") {
		t.Errorf("Start(nil store): err = %v, want store error", err)
	}
}

// TestPickServedGVR covers the served/storage version selection
// without booting envtest.
func TestPickServedGVR(t *testing.T) {
	cases := []struct {
		name      string
		versions  []apiextensionsv1.CustomResourceDefinitionVersion
		wantOK    bool
		wantVer   string
	}{
		{
			name: "storage version chosen over earlier served",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Served: true, Storage: false},
				{Name: "v1", Served: true, Storage: true},
			},
			wantOK: true, wantVer: "v1",
		},
		{
			name: "first served used when no storage flagged",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: false},
				{Name: "v2", Served: true, Storage: false},
			},
			wantOK: true, wantVer: "v1",
		},
		{
			name: "no served versions",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: false, Storage: false},
			},
			wantOK: false,
		},
		{
			name: "served=false skipped even if storage=true",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: false, Storage: true},
			},
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			crd := &apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "g.example.com",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Plural: "things", Kind: "Thing",
					},
					Versions: c.versions,
				},
			}
			gvr, kind, ok := pickServedGVR(crd)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !c.wantOK {
				return
			}
			if gvr.Version != c.wantVer {
				t.Errorf("version = %q, want %q", gvr.Version, c.wantVer)
			}
			if gvr.Resource != "things" || gvr.Group != "g.example.com" || kind != "Thing" {
				t.Errorf("gvr/kind = %v / %q, want g.example.com / things / Thing", gvr, kind)
			}
		})
	}
}

// TestPickServedGVR_NilCRD covers the defensive nil branch.
func TestPickServedGVR_NilCRD(t *testing.T) {
	if _, _, ok := pickServedGVR(nil); ok {
		t.Error("expected ok=false for nil CRD")
	}
}

// fakeDynamic satisfies dynamic.Interface for the nil-store error
// path test without dragging in client-go's full fake machinery.
type fakeDynamic struct{}

func (fakeDynamic) Resource(_ schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return nil
}
