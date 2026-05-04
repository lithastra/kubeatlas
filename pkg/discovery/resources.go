package discovery

import (
	"context"
	"errors"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// CoreGVRs is the canonical Phase 0 set of GroupVersionResources that
// KubeAtlas watches by default.
//
// Adding a new resource means:
//
//  1. Append an entry here.
//  2. Make sure the corresponding extractor in pkg/extractor knows
//     how to derive edges to or from it.
//  3. Document the resource and its edges in docs/architecture.md.
//
// ServiceAccount is included even though F-001's "15 core kinds" list
// does not name it explicitly: the USES_SERVICEACCOUNT edge requires
// SAs to exist as graph nodes, so we watch them as a 16th GVR.
var CoreGVRs = []schema.GroupVersionResource{
	// Core API group (v1).
	{Group: "", Version: "v1", Resource: "namespaces"},
	{Group: "", Version: "v1", Resource: "pods"},
	{Group: "", Version: "v1", Resource: "services"},
	{Group: "", Version: "v1", Resource: "configmaps"},
	{Group: "", Version: "v1", Resource: "secrets"},
	{Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
	{Group: "", Version: "v1", Resource: "serviceaccounts"},

	// apps/v1.
	{Group: "apps", Version: "v1", Resource: "deployments"},
	{Group: "apps", Version: "v1", Resource: "replicasets"},
	{Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "apps", Version: "v1", Resource: "daemonsets"},

	// batch/v1.
	{Group: "batch", Version: "v1", Resource: "jobs"},
	{Group: "batch", Version: "v1", Resource: "cronjobs"},

	// networking.k8s.io/v1.
	{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},

	// gateway.networking.k8s.io/v1 (optional CRDs; filtered at startup
	// when the Gateway API group is not installed).
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
}

// optionalGroups is the set of API groups whose absence on the cluster
// is expected (typically CRD-installed). Discovery checks these and
// drops missing GVRs rather than failing startup.
var optionalGroups = map[string]bool{
	"gateway.networking.k8s.io": true,
}

// FilterAvailableGVRs returns the subset of want that the cluster
// actually exposes. Missing optional GVRs are logged as warnings and
// dropped; missing required GVRs return an error.
func FilterAvailableGVRs(ctx context.Context, dc discovery.DiscoveryInterface, want []schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	out := make([]schema.GroupVersionResource, 0, len(want))
	for _, gvr := range want {
		ok, err := groupVersionAvailable(ctx, dc, gvr.GroupVersion())
		if err != nil {
			return nil, err
		}
		if !ok {
			if optionalGroups[gvr.Group] {
				slog.Warn("optional API group not available; skipping",
					"group", gvr.Group, "resource", gvr.Resource)
				continue
			}
			return nil, errors.New("required API group not available: " + gvr.GroupVersion().String())
		}
		out = append(out, gvr)
	}
	return out, nil
}

// groupVersionAvailable reports whether the apiserver advertises the
// given GroupVersion. Uses ServerResourcesForGroupVersion which is the
// cheapest path that distinguishes "absent" from "transient error".
func groupVersionAvailable(_ context.Context, dc discovery.DiscoveryInterface, gv schema.GroupVersion) (bool, error) {
	if _, err := dc.ServerResourcesForGroupVersion(gv.String()); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// NewDiscoveryFromClient is a tiny convenience that returns the
// discovery interface a Client was built against. The PoC Client
// stored a discovery.DiscoveryInterface for ServerPreferredResources;
// re-exposing it lets the informer manager probe the cluster at
// startup without a second kubeconfig load.
func NewDiscoveryFromClient(c *Client) discovery.DiscoveryInterface {
	return c.discovery
}
