// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestConstraintGVRFromTemplate(t *testing.T) {
	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "templates.gatekeeper.sh/v1",
		"kind":       "ConstraintTemplate",
		"metadata":   map[string]any{"name": "k8srequiredlabels"},
		"spec": map[string]any{
			"crd": map[string]any{
				"spec": map[string]any{
					"names": map[string]any{"kind": "K8sRequiredLabels"},
				},
			},
		},
	}}
	gvr, kind, ok := constraintGVRFromTemplate(tmpl)
	if !ok {
		t.Fatal("expected ok")
	}
	if kind != "K8sRequiredLabels" {
		t.Errorf("kind = %q, want K8sRequiredLabels", kind)
	}
	want := schema.GroupVersionResource{Group: "constraints.gatekeeper.sh", Version: "v1beta1", Resource: "k8srequiredlabels"}
	if gvr != want {
		t.Errorf("gvr = %+v, want %+v", gvr, want)
	}
}

func TestConstraintGVRFromTemplate_NoKind(t *testing.T) {
	tmpl := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
	if _, _, ok := constraintGVRFromTemplate(tmpl); ok {
		t.Error("expected !ok for a template with no kind")
	}
}

// nilRegistry is an ExtractorRegistry that emits nothing — the
// orchestration test cares only about informer (de)registration.
type nilRegistry struct{}

func (nilRegistry) ExtractAll(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
	return nil, nil
}

// TestDiscovery_RegistersConstraintInformerFromTemplate verifies the
// core difficulty: a ConstraintTemplate appearing at runtime makes the
// component register a dynamic informer for the Constraint kind it
// generates, and deleting the template removes it.
func TestDiscovery_RegistersConstraintInformerFromTemplate(t *testing.T) {
	dyn, apiext := startEnvOrSkip(t)

	// The ConstraintTemplate CRD must exist for its informer to list.
	createCRD(t, apiext, "templates.gatekeeper.sh", "v1", "ConstraintTemplate", "constrainttemplates", apiextensionsv1.ClusterScoped)

	store := memory.New()
	dynMgr := discovery.NewDynamicInformerManager(dyn)
	d := New(dyn, store, nilRegistry{}, dynMgr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Start(ctx) }()

	ctClient := dyn.Resource(constraintTemplateGVR)
	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "templates.gatekeeper.sh/v1",
		"kind":       "ConstraintTemplate",
		"metadata":   map[string]any{"name": "k8srequiredlabels"},
		"spec": map[string]any{
			"crd": map[string]any{"spec": map[string]any{"names": map[string]any{"kind": "K8sRequiredLabels"}}},
		},
	}}
	if _, err := ctClient.Create(ctx, tmpl, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ConstraintTemplate: %v", err)
	}

	want := schema.GroupVersionResource{Group: "constraints.gatekeeper.sh", Version: "v1beta1", Resource: "k8srequiredlabels"}
	waitFor(t, 15*time.Second, "constraint informer registered", func() bool { return dynMgr.Has(want) })

	if err := ctClient.Delete(ctx, "k8srequiredlabels", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete ConstraintTemplate: %v", err)
	}
	waitFor(t, 15*time.Second, "constraint informer removed", func() bool { return !dynMgr.Has(want) })
}

// --- envtest helpers -------------------------------------------------

func startEnvOrSkip(t *testing.T) (dynamic.Interface, apiextensionsclient.Interface) {
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
	return dyn, apiext
}

func createCRD(t *testing.T, c apiextensionsclient.Interface, group, version, kind, plural string, scope apiextensionsv1.ResourceScope) {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: plural, Kind: kind},
			Scope: scope,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: version, Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: ptr(true),
					},
				},
			}},
		},
	}
	if _, err := c.ApiextensionsV1().CustomResourceDefinitions().Create(context.Background(), crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD %s: %v", crd.Name, err)
	}
}

func ptr[T any](v T) *T { return &v }

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}
