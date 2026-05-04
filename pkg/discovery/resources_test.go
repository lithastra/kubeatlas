package discovery_test

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	kdiscovery "github.com/lithastra/kubeatlas/pkg/discovery"
)

// fakeDiscovery answers ServerResourcesForGroupVersion based on a
// fixed set of available GroupVersions. Any other GV returns a
// NotFound error, mirroring how a real apiserver behaves when the
// group is uninstalled.
type fakeDiscovery struct {
	discovery.DiscoveryInterface
	available map[string]bool
}

func (f *fakeDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if !f.available[gv] {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: gv, Resource: "groupversions"}, gv)
	}
	return &metav1.APIResourceList{GroupVersion: gv}, nil
}

func (f *fakeDiscovery) Fresh() bool { return true }
func (f *fakeDiscovery) Invalidate() {}

// errDiscovery returns a transient error for every probe.
type errDiscovery struct {
	discovery.DiscoveryInterface
}

func (e *errDiscovery) ServerResourcesForGroupVersion(_ string) (*metav1.APIResourceList, error) {
	return nil, errors.New("apiserver unavailable")
}

func TestFilterAvailableGVRs_KeepsAvailableSkipsOptional(t *testing.T) {
	dc := &fakeDiscovery{
		available: map[string]bool{
			"v1":                   true,
			"apps/v1":              true,
			"batch/v1":             true,
			"networking.k8s.io/v1": true,
			// gateway.networking.k8s.io/v1 deliberately omitted.
		},
	}
	got, err := kdiscovery.FilterAvailableGVRs(context.Background(), dc, kdiscovery.CoreGVRs)
	if err != nil {
		t.Fatalf("FilterAvailableGVRs: %v", err)
	}
	for _, gvr := range got {
		if gvr.Group == "gateway.networking.k8s.io" {
			t.Errorf("expected Gateway API to be filtered out, got %v", gvr)
		}
	}
	// All required GVRs (those not in the optionalGroups map) survive.
	wantCount := len(kdiscovery.CoreGVRs) - 2 // 2 gateway GVRs
	if len(got) != wantCount {
		t.Errorf("kept %d GVRs, want %d", len(got), wantCount)
	}
}

func TestFilterAvailableGVRs_FailsOnMissingRequired(t *testing.T) {
	dc := &fakeDiscovery{
		available: map[string]bool{
			"v1": true, // missing apps/v1, batch/v1, networking.k8s.io/v1
		},
	}
	_, err := kdiscovery.FilterAvailableGVRs(context.Background(), dc, kdiscovery.CoreGVRs)
	if err == nil {
		t.Fatal("expected error when required group missing, got nil")
	}
}

func TestFilterAvailableGVRs_PropagatesTransientError(t *testing.T) {
	dc := &errDiscovery{}
	_, err := kdiscovery.FilterAvailableGVRs(context.Background(), dc, kdiscovery.CoreGVRs)
	if err == nil {
		t.Fatal("expected transient error to propagate, got nil")
	}
}

func TestCoreGVRs_HasExpectedShape(t *testing.T) {
	// Sanity: the registry must include the 16 Phase 0 GVRs (15 core +
	// ServiceAccount). The exact list shifts with cluster API
	// availability at runtime, but the registry itself is stable.
	if len(kdiscovery.CoreGVRs) != 16 {
		t.Errorf("CoreGVRs length = %d, want 16", len(kdiscovery.CoreGVRs))
	}
	required := map[string]bool{
		"namespaces": true, "pods": true, "services": true,
		"configmaps": true, "secrets": true, "persistentvolumeclaims": true,
		"serviceaccounts": true, // explicit per spec
		"deployments":     true,
		"replicasets":     true,
		"statefulsets":    true,
		"daemonsets":      true,
		"jobs":            true,
		"cronjobs":        true,
		"ingresses":       true,
		"gateways":        true,
		"httproutes":      true,
	}
	seen := make(map[string]bool, len(kdiscovery.CoreGVRs))
	for _, gvr := range kdiscovery.CoreGVRs {
		seen[gvr.Resource] = true
	}
	for r := range required {
		if !seen[r] {
			t.Errorf("CoreGVRs missing %q", r)
		}
	}
}
