package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// nestedSlice walks raw at the given path and returns the slice at the
// leaf. Returns nil if any intermediate key is absent or any value
// along the way has the wrong type.
func nestedSlice(raw map[string]any, path ...string) []any {
	cur := any(raw)
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	out, _ := cur.([]any)
	return out
}

// nestedMap walks raw at the given path and returns the map at the
// leaf. Returns nil if any intermediate key is absent.
func nestedMap(raw map[string]any, path ...string) map[string]any {
	cur := any(raw)
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	out, _ := cur.(map[string]any)
	return out
}

// nestedString walks raw at the given path and returns the string at
// the leaf. Returns "" if any intermediate key is absent or the leaf
// is not a string.
func nestedString(raw map[string]any, path ...string) string {
	cur := any(raw)
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	out, _ := cur.(string)
	return out
}

// nestedStringMap walks raw at the given path and returns the
// string-keyed/string-valued map at the leaf. Empty map for missing
// path or wrong types.
func nestedStringMap(raw map[string]any, path ...string) map[string]string {
	m := nestedMap(raw, path...)
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// podTemplateContainers returns the containers slice from a workload
// resource's spec.template.spec.containers (Deployment, StatefulSet,
// DaemonSet, Job, CronJob) or spec.containers (raw Pod).
func podTemplateContainers(r graph.Resource) []any {
	if c := nestedSlice(r.Raw, "spec", "template", "spec", "containers"); len(c) > 0 {
		return c
	}
	if c := nestedSlice(r.Raw, "spec", "jobTemplate", "spec", "template", "spec", "containers"); len(c) > 0 {
		return c // CronJob
	}
	return nestedSlice(r.Raw, "spec", "containers")
}

// podTemplateVolumes returns the volumes slice from a workload's
// spec.template.spec.volumes or a raw Pod's spec.volumes.
func podTemplateVolumes(r graph.Resource) []any {
	if v := nestedSlice(r.Raw, "spec", "template", "spec", "volumes"); len(v) > 0 {
		return v
	}
	if v := nestedSlice(r.Raw, "spec", "jobTemplate", "spec", "template", "spec", "volumes"); len(v) > 0 {
		return v // CronJob
	}
	return nestedSlice(r.Raw, "spec", "volumes")
}

// podTemplateMeta returns the spec.template.metadata map for workloads,
// or the resource's own metadata map for raw Pods. Used by SELECTS
// matching to look at pod labels.
func podTemplateMeta(r graph.Resource) map[string]any {
	if m := nestedMap(r.Raw, "spec", "template", "metadata"); m != nil {
		return m
	}
	if m := nestedMap(r.Raw, "spec", "jobTemplate", "spec", "template", "metadata"); m != nil {
		return m
	}
	return nestedMap(r.Raw, "metadata")
}

// podSpec returns the pod template spec for workloads or the
// resource's own spec for raw Pods.
func podSpec(r graph.Resource) map[string]any {
	if m := nestedMap(r.Raw, "spec", "template", "spec"); m != nil {
		return m
	}
	if m := nestedMap(r.Raw, "spec", "jobTemplate", "spec", "template", "spec"); m != nil {
		return m
	}
	return nestedMap(r.Raw, "spec")
}

// labelsMatch reports whether every key/value pair in want is present
// in have. An empty want matches nothing (Service.spec.selector with
// no entries does not select anything).
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

// hasPodTemplate reports whether r is a workload kind that carries a
// pod template (so SELECTS / config / volume etc. should walk it).
func hasPodTemplate(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ReplicaSet":
		return true
	}
	return false
}
