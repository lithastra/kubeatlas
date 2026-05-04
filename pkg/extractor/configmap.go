package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// ConfigMapExtractor emits USES_CONFIGMAP edges from workloads (and
// raw Pods) to every ConfigMap they reference. Three reference styles
// are handled:
//
//   - container.envFrom[].configMapRef.name
//   - container.env[].valueFrom.configMapKeyRef.name
//   - volumes[].configMap.name
type ConfigMapExtractor struct{}

func (ConfigMapExtractor) Type() graph.EdgeType { return graph.EdgeTypeUsesConfigMap }

func (ConfigMapExtractor) Extract(r graph.Resource, _ []graph.Resource) []graph.Edge {
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
		to := graph.Resource{Kind: "ConfigMap", Name: name, Namespace: ns}.ID()
		if _, ok := seen[to]; ok {
			return nil
		}
		seen[to] = struct{}{}
		return []graph.Edge{{From: from, To: to, Type: graph.EdgeTypeUsesConfigMap}}
	}

	var edges []graph.Edge
	for _, c := range podTemplateContainers(r) {
		cmap, _ := c.(map[string]any)
		for _, ef := range nestedSlice(cmap, "envFrom") {
			efm, _ := ef.(map[string]any)
			edges = append(edges, emit(nestedString(efm, "configMapRef", "name"))...)
		}
		for _, e := range nestedSlice(cmap, "env") {
			em, _ := e.(map[string]any)
			edges = append(edges, emit(nestedString(em, "valueFrom", "configMapKeyRef", "name"))...)
		}
	}
	for _, v := range podTemplateVolumes(r) {
		vmap, _ := v.(map[string]any)
		edges = append(edges, emit(nestedString(vmap, "configMap", "name"))...)
	}
	return edges
}
