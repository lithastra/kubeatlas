package extractor

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// SecretExtractor emits USES_SECRET edges from non-Secret objects to every
// Secret name they reference. KubeAtlas deliberately does not list or watch
// Secret objects; the informer materialises each target as a reference-only
// placeholder. Reference styles include:
//
//   - container.envFrom[].secretRef.name
//   - container.env[].valueFrom.secretKeyRef.name
//   - volumes[].secret.secretName
//   - spec.imagePullSecrets[].name
//   - ServiceAccount.secrets[] / imagePullSecrets[]
//   - Ingress.tls[].secretName
//   - Gateway listener TLS certificateRefs whose kind is Secret
type SecretExtractor struct{}

func (SecretExtractor) Type() graph.EdgeType { return graph.EdgeTypeUsesSecret }

func (SecretExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	from := r.ID()
	seen := make(map[string]struct{})
	emit := func(namespace, name string) []graph.Edge {
		if name == "" {
			return nil
		}
		to := graph.Resource{Kind: "Secret", Name: name, Namespace: namespace, ClusterID: r.ClusterID}.ID()
		if _, ok := seen[to]; ok {
			return nil
		}
		seen[to] = struct{}{}
		return []graph.Edge{{From: from, To: to, Type: graph.EdgeTypeUsesSecret}}
	}

	var edges []graph.Edge
	if r.Kind == "Pod" || hasPodTemplate(r.Kind) {
		spec := podSpec(r)
		for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
			for _, c := range nestedSlice(spec, field) {
				cmap, _ := c.(map[string]any)
				for _, ef := range nestedSlice(cmap, "envFrom") {
					efm, _ := ef.(map[string]any)
					edges = append(edges, emit(r.Namespace, nestedString(efm, "secretRef", "name"))...)
				}
				for _, e := range nestedSlice(cmap, "env") {
					em, _ := e.(map[string]any)
					edges = append(edges, emit(r.Namespace, nestedString(em, "valueFrom", "secretKeyRef", "name"))...)
				}
			}
		}
		for _, v := range podTemplateVolumes(r) {
			vmap, _ := v.(map[string]any)
			edges = append(edges, emit(r.Namespace, nestedString(vmap, "secret", "secretName"))...)
			for _, source := range nestedSlice(vmap, "projected", "sources") {
				sm, _ := source.(map[string]any)
				edges = append(edges, emit(r.Namespace, nestedString(sm, "secret", "name"))...)
			}
		}
		for _, p := range nestedSlice(spec, "imagePullSecrets") {
			pmap, _ := p.(map[string]any)
			edges = append(edges, emit(r.Namespace, nestedString(pmap, "name"))...)
		}
	}

	if r.Kind == "ServiceAccount" {
		for _, field := range []string{"secrets", "imagePullSecrets"} {
			for _, ref := range nestedSlice(r.Raw, field) {
				refMap, _ := ref.(map[string]any)
				edges = append(edges, emit(r.Namespace, nestedString(refMap, "name"))...)
			}
		}
	}

	if r.Kind == "Ingress" {
		for _, tls := range nestedSlice(r.Raw, "spec", "tls") {
			tlsMap, _ := tls.(map[string]any)
			edges = append(edges, emit(r.Namespace, nestedString(tlsMap, "secretName"))...)
		}
	}

	if r.Kind == "Gateway" {
		for _, listener := range nestedSlice(r.Raw, "spec", "listeners") {
			listenerMap, _ := listener.(map[string]any)
			for _, ref := range nestedSlice(listenerMap, "tls", "certificateRefs") {
				refMap, _ := ref.(map[string]any)
				kind := nestedString(refMap, "kind")
				group := nestedString(refMap, "group")
				if (kind == "" || kind == "Secret") && (group == "" || group == "core") {
					ns := nestedString(refMap, "namespace")
					if ns == "" {
						ns = r.Namespace
					}
					edges = append(edges, emit(ns, nestedString(refMap, "name"))...)
				}
			}
		}
	}
	return edges, nil
}
