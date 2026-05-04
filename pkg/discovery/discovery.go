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

// Client wraps the Kubernetes discovery and dynamic clients, and caches the
// raw collected objects so dependency extraction can read spec-level fields
// that graph.Resource does not carry.
type Client struct {
	discovery discovery.DiscoveryInterface
	dynamic   dynamic.Interface
	objects   []unstructured.Unstructured
}

// NewClient builds a Client from the default kubeconfig.
// Resolution order: $KUBECONFIG, then ~/.kube/config.
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
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate user home: %w", err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// CollectAll walks every namespaced API resource the cluster exposes and
// returns one graph.Resource per object found. The raw unstructured objects
// are cached on the Client for ExtractDependencies.
func (c *Client) CollectAll() ([]graph.Resource, error) {
	apiLists, err := c.discovery.ServerPreferredResources()
	// ServerPreferredResources may return partial results with a non-nil err
	// (e.g. an aggregated API server is down). Warn and proceed with what we got.
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: discovery returned partial results: %v\n", err)
	}

	var resources []graph.Resource
	var raws []unstructured.Unstructured

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
				raws = append(raws, u)
			}
		}
	}

	c.objects = raws
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

// ExtractDependencies derives edges from the objects last collected by
// CollectAll.
func (c *Client) ExtractDependencies() []graph.Edge {
	var edges []graph.Edge
	for i := range c.objects {
		u := &c.objects[i]
		kind := u.GetKind()
		from := graph.Resource{Kind: kind, Name: u.GetName(), Namespace: u.GetNamespace()}.ID()

		edges = append(edges, ownerRefEdges(u, from)...)

		switch kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			edges = append(edges, workloadConfigEdges(u, from)...)
		case "Service":
			edges = append(edges, serviceSelectorEdges(u, c.objects)...)
		case "Ingress":
			edges = append(edges, ingressBackendEdges(u, from)...)
		case "HTTPRoute":
			edges = append(edges, httpRouteEdges(u, from)...)
		}
	}
	return edges
}

func ownerRefEdges(u *unstructured.Unstructured, from string) []graph.Edge {
	ns := u.GetNamespace()
	var edges []graph.Edge
	for _, o := range u.GetOwnerReferences() {
		to := graph.Resource{Kind: o.Kind, Name: o.Name, Namespace: ns}.ID()
		edges = append(edges, graph.Edge{From: from, To: to, Relation: "ownerRef"})
	}
	return edges
}

func workloadConfigEdges(u *unstructured.Unstructured, from string) []graph.Edge {
	ns := u.GetNamespace()
	containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
	var edges []graph.Edge
	for _, ci := range containers {
		cmap, ok := ci.(map[string]interface{})
		if !ok {
			continue
		}

		envFrom, _, _ := unstructured.NestedSlice(cmap, "envFrom")
		for _, ef := range envFrom {
			efm, ok := ef.(map[string]interface{})
			if !ok {
				continue
			}
			if name, found, _ := unstructured.NestedString(efm, "configMapRef", "name"); found && name != "" {
				edges = append(edges, graph.Edge{
					From:     from,
					To:       graph.Resource{Kind: "ConfigMap", Name: name, Namespace: ns}.ID(),
					Relation: "configMapRef",
				})
			}
			if name, found, _ := unstructured.NestedString(efm, "secretRef", "name"); found && name != "" {
				edges = append(edges, graph.Edge{
					From:     from,
					To:       graph.Resource{Kind: "Secret", Name: name, Namespace: ns}.ID(),
					Relation: "secretRef",
				})
			}
		}

		env, _, _ := unstructured.NestedSlice(cmap, "env")
		for _, e := range env {
			em, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			if name, found, _ := unstructured.NestedString(em, "valueFrom", "configMapKeyRef", "name"); found && name != "" {
				edges = append(edges, graph.Edge{
					From:     from,
					To:       graph.Resource{Kind: "ConfigMap", Name: name, Namespace: ns}.ID(),
					Relation: "configMapRef",
				})
			}
			if name, found, _ := unstructured.NestedString(em, "valueFrom", "secretKeyRef", "name"); found && name != "" {
				edges = append(edges, graph.Edge{
					From:     from,
					To:       graph.Resource{Kind: "Secret", Name: name, Namespace: ns}.ID(),
					Relation: "secretRef",
				})
			}
		}
	}
	return edges
}

func serviceSelectorEdges(svc *unstructured.Unstructured, all []unstructured.Unstructured) []graph.Edge {
	ns := svc.GetNamespace()
	selector, found, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
	if !found || len(selector) == 0 {
		return nil
	}
	from := graph.Resource{Kind: "Service", Name: svc.GetName(), Namespace: ns}.ID()

	var edges []graph.Edge
	for i := range all {
		t := &all[i]
		if t.GetNamespace() != ns {
			continue
		}
		switch t.GetKind() {
		case "Pod":
			if labelsMatch(t.GetLabels(), selector) {
				edges = append(edges, graph.Edge{
					From:     from,
					To:       graph.Resource{Kind: "Pod", Name: t.GetName(), Namespace: ns}.ID(),
					Relation: "selector",
				})
			}
		case "Deployment", "StatefulSet", "DaemonSet":
			tlabels, _, _ := unstructured.NestedStringMap(t.Object, "spec", "template", "metadata", "labels")
			if labelsMatch(tlabels, selector) {
				edges = append(edges, graph.Edge{
					From:     from,
					To:       graph.Resource{Kind: t.GetKind(), Name: t.GetName(), Namespace: ns}.ID(),
					Relation: "selector",
				})
			}
		}
	}
	return edges
}

func labelsMatch(have, want map[string]string) bool {
	if len(want) == 0 {
		return false
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func ingressBackendEdges(ing *unstructured.Unstructured, from string) []graph.Edge {
	ns := ing.GetNamespace()
	rules, _, _ := unstructured.NestedSlice(ing.Object, "spec", "rules")
	var edges []graph.Edge
	for _, r := range rules {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		paths, _, _ := unstructured.NestedSlice(rm, "http", "paths")
		for _, p := range paths {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			name, found, _ := unstructured.NestedString(pm, "backend", "service", "name")
			if !found || name == "" {
				continue
			}
			edges = append(edges, graph.Edge{
				From:     from,
				To:       graph.Resource{Kind: "Service", Name: name, Namespace: ns}.ID(),
				Relation: "backend",
			})
		}
	}
	return edges
}

func httpRouteEdges(rt *unstructured.Unstructured, from string) []graph.Edge {
	ns := rt.GetNamespace()
	var edges []graph.Edge

	parents, _, _ := unstructured.NestedSlice(rt.Object, "spec", "parentRefs")
	for _, p := range parents {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		name, found, _ := unstructured.NestedString(pm, "name")
		if !found || name == "" {
			continue
		}
		kind := "Gateway"
		if k, ok, _ := unstructured.NestedString(pm, "kind"); ok && k != "" {
			kind = k
		}
		pns := ns
		if n, ok, _ := unstructured.NestedString(pm, "namespace"); ok && n != "" {
			pns = n
		}
		edges = append(edges, graph.Edge{
			From:     from,
			To:       graph.Resource{Kind: kind, Name: name, Namespace: pns}.ID(),
			Relation: "parentRef",
		})
	}

	rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
	for _, r := range rules {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		backends, _, _ := unstructured.NestedSlice(rm, "backendRefs")
		for _, b := range backends {
			bm, ok := b.(map[string]interface{})
			if !ok {
				continue
			}
			name, found, _ := unstructured.NestedString(bm, "name")
			if !found || name == "" {
				continue
			}
			kind := "Service"
			if k, ok, _ := unstructured.NestedString(bm, "kind"); ok && k != "" {
				kind = k
			}
			bns := ns
			if n, ok, _ := unstructured.NestedString(bm, "namespace"); ok && n != "" {
				bns = n
			}
			edges = append(edges, graph.Edge{
				From:     from,
				To:       graph.Resource{Kind: kind, Name: name, Namespace: bns}.ID(),
				Relation: "backendRef",
			})
		}
	}
	return edges
}
