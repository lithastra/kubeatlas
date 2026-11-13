package discovery

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// autoscaling/v2 — HorizontalPodAutoscaler. The SCALES extractor
	// reads spec.scaleTargetRef and emits one edge per HPA to its
	// target workload. v2 has been the GA API since Kubernetes 1.23;
	// v1 / v2beta* HPAs still appear here because the apiserver
	// serves them through the same v2 endpoint by default.
	{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},

	// networking.k8s.io/v1.
	{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	// P3-T1 (F-109) — NetworkPolicy edges (SELECTS_NP / ALLOWS_FROM /
	// ALLOWS_TO) need the policy itself as a graph node before the
	// extractor can resolve its podSelector + peers.
	{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},

	// gateway.networking.k8s.io/v1 (optional CRDs; filtered at startup
	// when the Gateway API group is not installed).
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},

	// rbac.authorization.k8s.io/v1 — Phase 2 P2-T14. Watching all
	// four lets the BindsSubject / BindsRole extractors emit
	// SA -> RoleBinding -> Role chains. Roles + ClusterRoles are
	// graph nodes so the UI can render them even when no binding
	// references them.
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},

	// Kyverno policy integration (F-205). The kyverno.io and
	// wgpolicyk8s.io groups are install-time CRDs — filtered at startup
	// when Kyverno is not installed. The KyvernoExtractor reads
	// (Cluster)Policy match rules to emit ENFORCES edges and overlays
	// (Cluster)PolicyReport results as the edge's pass/fail status.
	{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"},
	{Group: "kyverno.io", Version: "v1", Resource: "policies"},
	{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"},
	{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"},
}

// Only canonical endpoints have an unambiguous repair kind when their initial
// list is empty. Unknown custom WithGVRs inputs fail closed instead of guessing
// a Kind from a plural (ConfigMap/PVC/RBAC names cannot be safely title-cased).
func coreRepairKind(gvr schema.GroupVersionResource) string {
	if !slices.Contains(CoreGVRs, gvr) {
		return ""
	}
	return map[string]string{
		"namespaces": "Namespace", "pods": "Pod", "services": "Service", "configmaps": "ConfigMap",
		"persistentvolumeclaims": "PersistentVolumeClaim", "serviceaccounts": "ServiceAccount",
		"deployments": "Deployment", "replicasets": "ReplicaSet", "statefulsets": "StatefulSet", "daemonsets": "DaemonSet",
		"jobs": "Job", "cronjobs": "CronJob", "horizontalpodautoscalers": "HorizontalPodAutoscaler",
		"ingresses": "Ingress", "networkpolicies": "NetworkPolicy", "gateways": "Gateway", "httproutes": "HTTPRoute",
		"roles": "Role", "rolebindings": "RoleBinding", "clusterroles": "ClusterRole", "clusterrolebindings": "ClusterRoleBinding",
		"clusterpolicies": "ClusterPolicy", "policies": "Policy", "policyreports": "PolicyReport", "clusterpolicyreports": "ClusterPolicyReport",
	}[gvr.Resource]
}

// optionalGroups is the set of API groups whose absence on the cluster
// is expected (typically CRD-installed). Discovery checks these and
// drops missing GVRs rather than failing startup.
var optionalGroups = map[string]bool{
	"gateway.networking.k8s.io": true,
	"kyverno.io":                true,
	"wgpolicyk8s.io":            true,
}

// FilterAvailableGVRs returns the subset of want that the cluster
// advertises with a supported shape. Missing/unsupported optional GVRs are
// dropped; required ones return an error. Ambiguous or malformed target metadata
// fails the selection without returning a partial startup set.
func FilterAvailableGVRs(ctx context.Context, dc discovery.DiscoveryInterface, want []schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	if dc == nil {
		return nil, errors.New("nil API discovery client")
	}
	if len(want) > 256 {
		return nil, errors.New("excessive startup API discovery scope")
	}
	out := make([]schema.GroupVersionResource, 0, len(want))
	cache := map[string]*metav1.APIResourceList{}
	for _, gvr := range want {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		gv := gvr.GroupVersion().String()
		resources, checked := cache[gv]
		if !checked {
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			if contextual, ok := dc.(interface {
				ServerResourcesForGroupVersionWithContext(context.Context, string) (*metav1.APIResourceList, error)
			}); ok {
				resources, err = contextual.ServerResourcesForGroupVersionWithContext(probeCtx, gv)
			} else {
				resources, err = dc.ServerResourcesForGroupVersion(gv)
			}
			contextErr := probeCtx.Err()
			cancel()
			if contextErr != nil {
				return nil, contextErr
			}
			if err != nil && !apierrors.IsNotFound(err) {
				return nil, err
			}
			if err != nil {
				resources = nil
			} else if resources == nil || resources.GroupVersion != gv {
				return nil, errors.New("invalid startup API discovery response")
			}
			cache[gv] = resources
		}
		ok, err := startupResourceAvailable(ctx, resources, gvr)
		if err != nil {
			return nil, err
		}
		if !ok {
			if optionalGroups[gvr.Group] {
				slog.Warn("optional resource API unavailable; skipping",
					"group", gvr.Group, "resource", gvr.Resource)
				continue
			}
			return nil, errors.New("required resource API not available: " + gvr.String())
		}
		out = append(out, gvr)
	}
	return out, nil
}

func startupResourceAvailable(ctx context.Context, resources *metav1.APIResourceList, gvr schema.GroupVersionResource) (bool, error) {
	var target *metav1.APIResource
	if resources != nil {
		for i := range resources.APIResources {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			resource := &resources.APIResources[i]
			if resource.Name != gvr.Resource {
				continue
			}
			if target != nil {
				return false, errors.New("ambiguous startup resource API discovery response")
			}
			target = resource
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if target == nil {
		return false, nil
	}
	descriptor := operations.APIResourceDescriptor{Group: gvr.Group, Version: gvr.Version, Resource: target.Name, Kind: target.Kind, Namespaced: target.Namespaced}
	if !operations.ValidAPIResourceDescriptor(descriptor) || (target.Group != "" && target.Group != gvr.Group) || (target.Version != "" && target.Version != gvr.Version) {
		return false, errors.New("invalid startup resource API discovery response")
	}
	if kind := coreRepairKind(gvr); kind != "" {
		namespaced := true
		switch kind {
		case "Namespace", "ClusterRole", "ClusterRoleBinding", "ClusterPolicy", "ClusterPolicyReport":
			namespaced = false
		}
		if target.Kind != kind || target.Namespaced != namespaced {
			return false, nil
		}
	}
	return slices.Contains(target.Verbs, "list") && slices.Contains(target.Verbs, "watch"), nil
}

// GroupVersionAvailable reports whether the apiserver serves gv. It is
// the exported form of the internal probe FilterAvailableGVRs uses, for
// optional components (e.g. gatekeeper.Discovery) that gate an informer
// on its operator's CRD being installed — so a cluster without that
// operator stays quiet instead of spinning a doomed watch.
func GroupVersionAvailable(ctx context.Context, dc discovery.DiscoveryInterface, gv schema.GroupVersion) (bool, error) {
	return groupVersionAvailable(ctx, dc, gv)
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
