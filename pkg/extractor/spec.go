package extractor

import (
	"errors"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

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

// selectorStringMap keeps omitted/null selector fields compatible with empty
// maps, but rejects malformed non-null objects and values atomically. Silently
// dropping a condition would broaden the selector. Errors contain no input data.
func selectorStringMap(raw map[string]any, path ...string) (map[string]string, error) {
	var cur any = raw
	for _, key := range path {
		if cur == nil {
			return nil, nil
		}
		object, ok := cur.(map[string]any)
		if !ok {
			return nil, errors.New("label selector must be an object")
		}
		cur = object[key]
	}
	if cur == nil {
		return nil, nil
	}
	object, ok := cur.(map[string]any)
	if !ok {
		return nil, errors.New("label selector must be an object")
	}
	out := make(map[string]string, len(object))
	for key, value := range object {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("label selector values must be strings")
		}
		out[key] = text
	}
	return out, nil
}

// selectorNamespaces preserves omitted/null/empty lists, but rejects malformed
// lists atomically. Dropping all invalid entries would turn a constraint into
// an unrestricted namespace query. Strings retain their existing literal meaning.
func selectorNamespaces(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("namespace selector must be a list")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		namespace, ok := item.(string)
		if !ok || namespace == "" {
			return nil, errors.New("namespace selector entries must be nonempty strings")
		}
		out = append(out, namespace)
	}
	return out, nil
}

// podTemplateContainers returns one container field (regular, init or ephemeral)
// from the kind's Pod spec, without falling through empty lists to other paths.
func podTemplateContainers(r graph.Resource, field string) []any {
	return nestedSlice(podSpec(r), field)
}

// podTemplateVolumes returns volumes from the same kind-selected Pod spec
// used by container references.
func podTemplateVolumes(r graph.Resource) []any {
	return nestedSlice(podSpec(r), "volumes")
}

// podTemplateMeta selects the kind's Pod metadata. Missing template metadata
// must not substitute controller labels or another kind's template path.
func podTemplateMeta(r graph.Resource) map[string]any {
	switch r.Kind {
	case "Pod":
		return nestedMap(r.Raw, "metadata")
	case "CronJob":
		return nestedMap(r.Raw, "spec", "jobTemplate", "spec", "template", "metadata")
	default:
		if hasPodTemplate(r.Kind) {
			return nestedMap(r.Raw, "spec", "template", "metadata")
		}
		return nil
	}
}

// podSpec selects the builtin kind's canonical path. Missing/empty fields must
// not redirect collection to a Pod-like object at another path.
func podSpec(r graph.Resource) map[string]any {
	switch r.Kind {
	case "Pod":
		return nestedMap(r.Raw, "spec")
	case "CronJob":
		return nestedMap(r.Raw, "spec", "jobTemplate", "spec", "template", "spec")
	default:
		if hasPodTemplate(r.Kind) {
			return nestedMap(r.Raw, "spec", "template", "spec")
		}
		return nil
	}
}

// labelsMatch reports whether every key/value pair in want is present
// in have. An empty want matches nothing (Service.spec.selector with
// no entries does not select anything).
func labelsMatch(have, want map[string]string) bool {
	if len(want) == 0 {
		return false
	}
	for k, v := range want {
		actual, exists := have[k]
		if !exists || actual != v {
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
