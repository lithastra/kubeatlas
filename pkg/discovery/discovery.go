package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// skippedGVRs lists resources that are transient by nature and don't belong
// in a dependency graph. They have no "depends-on" semantics — they're
// observability/auth machinery, not architectural building blocks.
//
// Key format: "<group>/<version>/<resource>". The core API group's group is
// empty, so its keys start with the version (e.g. "v1/events").
var skippedGVRs = map[string]bool{
	"v1/events":                                        true,
	"events.k8s.io/v1/events":                          true,
	"coordination.k8s.io/v1/leases":                    true,
	"authentication.k8s.io/v1/tokenreviews":            true,
	"authorization.k8s.io/v1/subjectaccessreviews":     true,
	"authorization.k8s.io/v1/selfsubjectaccessreviews": true,
	// Endpoints and EndpointSlices are auto-derived from Service selectors
	// by the control plane; they carry no architectural intent. v1/endpoints
	// also emits a deprecation warning in k8s 1.33+ (use EndpointSlice).
	"v1/endpoints":                       true,
	"discovery.k8s.io/v1/endpointslices": true,
	// ComponentStatus is etcd/scheduler/controller-manager health
	// (observability), not an architectural building block, and is
	// deprecated since k8s 1.19.
	"v1/componentstatuses": true,
	// Bindings are write-only (used by the scheduler to bind a Pod to
	// a Node); they have no list semantics worth graphing.
	"v1/bindings": true,
}

// Client wraps the Kubernetes discovery and dynamic clients used by
// CollectAll (one-shot snapshot) and by the informer pipeline. Per-
// resource spec data needed by extractors travels on graph.Resource's
// Raw field; the client no longer caches the raw object list itself.
type Client struct {
	discovery discovery.DiscoveryInterface
	dynamic   dynamic.Interface
}

// NewClient builds a Client from the most appropriate source for the
// runtime environment.
//
// Resolution order:
//
//  1. $KUBECONFIG (explicit override — wins everywhere).
//  2. In-cluster service account (when KUBERNETES_SERVICE_HOST is set,
//     i.e. running inside a Pod).
//  3. ~/.kube/config (default for local dev / -once mode on a laptop).
//
// The in-cluster step is what lets the Helm-installed Pod work: the
// distroless image has no $HOME/.kube/config and no UserHomeDir, so
// the previous "always read kubeconfig" path crashed on startup.
func NewClient() (*Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build discovery client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	return &Client{discovery: dc, dynamic: dyn}, nil
}

func loadConfig() (*rest.Config, error) {
	if explicit := os.Getenv("KUBECONFIG"); explicit != "" {
		return clientcmd.BuildConfigFromFlags("", explicit)
	}
	// In-cluster: client-go sets KUBERNETES_SERVICE_HOST in every Pod.
	// Prefer it over a stray ~/.kube/config so a developer running
	// `kubectl exec` into the Pod can't accidentally point KubeAtlas
	// at the wrong cluster.
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		cfg, err := rest.InClusterConfig()
		if err == nil {
			return cfg, nil
		}
		// Fall through to kubeconfig if in-cluster discovery fails for
		// an unexpected reason (e.g. a busted SA mount); the error from
		// kubeconfig is usually clearer than InClusterConfig's.
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("no $KUBECONFIG and no in-cluster config; locate user home failed: %w", err)
	}
	return clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
}

// CollectAll walks every API resource the cluster exposes (namespaced
// and cluster-scoped) and returns one graph.Resource per object found.
// Spec-level data needed by extractors travels on each Resource's Raw
// field; pkg/extractor consumes it.
func (c *Client) CollectAll() ([]graph.Resource, error) {
	apiLists, err := c.discovery.ServerPreferredResources()
	// ServerPreferredResources may return partial results with a non-nil err
	// (e.g. an aggregated API server is down). Warn and proceed with what we got.
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: discovery returned partial results: %v\n", err)
	}

	var resources []graph.Resource

	for _, list := range apiLists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: parse groupVersion %q: %v\n", list.GroupVersion, err)
			continue
		}
		for _, r := range list.APIResources {
			// Skip subresources (status, scale, etc.) — they appear with a
			// "/" in the resource name.
			if strings.Contains(r.Name, "/") {
				continue
			}
			if !hasVerb(r.Verbs, "list") {
				continue
			}
			key := strings.TrimPrefix(gv.Group+"/"+gv.Version+"/"+r.Name, "/")
			if skippedGVRs[key] {
				continue
			}
			gvr := gv.WithResource(r.Name)
			ulist, err := c.dynamic.Resource(gvr).List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: list %s: %v\n", gvr, err)
				continue
			}
			for i := range ulist.Items {
				u := ulist.Items[i]
				resources = append(resources, toResource(&u, r.Kind))
			}
		}
	}

	return resources, nil
}

func toResource(u *unstructured.Unstructured, kind string) graph.Resource {
	r := graph.Resource{
		Kind:            kind,
		Name:            u.GetName(),
		Namespace:       u.GetNamespace(),
		Labels:          u.GetLabels(),
		GroupVersion:    u.GetAPIVersion(),
		UID:             u.GetUID(),
		Annotations:     u.GetAnnotations(),
		ResourceVersion: u.GetResourceVersion(),
		Raw:             u.DeepCopy().Object,
	}
	if owners := u.GetOwnerReferences(); len(owners) > 0 {
		r.OwnerReferences = make([]graph.OwnerRef, 0, len(owners))
		for _, o := range owners {
			r.OwnerReferences = append(r.OwnerReferences, graph.OwnerRef{
				Kind: o.Kind,
				Name: o.Name,
				UID:  o.UID,
			})
		}
	}
	return r
}

func hasVerb(verbs []string, target string) bool {
	for _, v := range verbs {
		if v == target {
			return true
		}
	}
	return false
}
