package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// SecretExtractor emits USES_SECRET edges from workloads (and raw
// Pods) to every Secret they reference. Reference styles:
//
//   - container.envFrom[].secretRef.name
//   - container.env[].valueFrom.secretKeyRef.name
//   - volumes[].secret.secretName
//   - spec.imagePullSecrets[].name
type SecretExtractor struct{}

func (SecretExtractor) Type() graph.EdgeType { return graph.EdgeTypeUsesSecret }

func (SecretExtractor) Extract(r graph.Resource, _ []graph.Resource) []graph.Edge {
	if r.Kind != "Pod" && !hasPodTemplate(r.Kind) {
		return nil
	}
	from := r.ID()
	ns := r.Namespace
	seen := make(map[string]struct{})
	emit := func(name string) []graph.Edge {
		if name == "" {
			return nil
		}
		to := graph.Resource{Kind: "Secret", Name: name, Namespace: ns}.ID()
		if _, ok := seen[to]; ok {
			return nil
		}
		seen[to] = struct{}{}
		return []graph.Edge{{From: from, To: to, Type: graph.EdgeTypeUsesSecret}}
	}

	var edges []graph.Edge
	for _, c := range podTemplateContainers(r) {
		cmap, _ := c.(map[string]any)
		for _, ef := range nestedSlice(cmap, "envFrom") {
			efm, _ := ef.(map[string]any)
			edges = append(edges, emit(nestedString(efm, "secretRef", "name"))...)
		}
		for _, e := range nestedSlice(cmap, "env") {
			em, _ := e.(map[string]any)
			edges = append(edges, emit(nestedString(em, "valueFrom", "secretKeyRef", "name"))...)
		}
	}
	for _, v := range podTemplateVolumes(r) {
		vmap, _ := v.(map[string]any)
		edges = append(edges, emit(nestedString(vmap, "secret", "secretName"))...)
	}
	for _, p := range nestedSlice(podSpec(r), "imagePullSecrets") {
		pmap, _ := p.(map[string]any)
		edges = append(edges, emit(nestedString(pmap, "name"))...)
	}
	return edges
}
