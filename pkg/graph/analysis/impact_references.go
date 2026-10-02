// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Reference interpretation is separately bounded from traversal. These limits
// are shared across a query, including ignored entries in supported arrays.
const (
	MaxImpactReferenceEntries = 100_000
	MaxImpactReferenceBytes   = 16 << 20
)

var ErrImpactReferenceLimit = errors.New("impact: reference interpretation budget exceeded")

// ImpactReferenceUse describes a declaration, not a running container, a value,
// a missing key, or an outage. Paths are JSON Pointers into the bound source
// version. MountFieldPath identifies the volumeMount, not its mountPath value.
// Required/optional concerns object/key lookup only. Optional never means that
// a change is harmless to the application. Refresh classifies the declaration's
// update mechanism, not target mutability, watch health, or application reload.
type ImpactReferenceUse struct {
	FieldPath      string `json:"fieldPath"`
	MountFieldPath string `json:"mountFieldPath,omitempty"`
	Mode           string `json:"mode"`
	Optional       string `json:"optional"`
	Refresh        string `json:"refresh"`
}

// ImpactReferenceEvidence is a sidecar for one stored edge used by a returned
// representative path. Multiple fields/mounts are retained even when graph
// extraction deduplicated their edge. Status is "version_bound" or "unknown";
// absence of source evidence never becomes an inferred field or default flag.
// Source UID/version belong to the consumer, never to the referenced Secret.
type ImpactReferenceEvidence struct {
	From                  string               `json:"from"`
	To                    string               `json:"to"`
	Type                  graph.EdgeType       `json:"type"`
	SourceUID             string               `json:"sourceUID,omitempty"`
	SourceResourceVersion string               `json:"sourceResourceVersion,omitempty"`
	Status                string               `json:"status"`
	Reason                string               `json:"reason,omitempty"`
	Uses                  []ImpactReferenceUse `json:"uses"`
}

type impactReferenceTarget struct {
	id       string
	edgeType graph.EdgeType
}

type impactReferenceSource struct {
	reason string
	uses   map[impactReferenceTarget][]ImpactReferenceUse
}

type impactReferenceReader struct {
	entries int
	bytes   int
	sources map[string]*impactReferenceSource
}

func newImpactReferenceReader() *impactReferenceReader {
	return &impactReferenceReader{
		entries: MaxImpactReferenceEntries, bytes: MaxImpactReferenceBytes,
		sources: make(map[string]*impactReferenceSource),
	}
}

func isImpactReference(edgeType graph.EdgeType) bool {
	return edgeType == graph.EdgeTypeUsesConfigMap || edgeType == graph.EdgeTypeUsesSecret || edgeType == graph.EdgeTypeMountsVolume
}

// explain consumes only the caller's immutable, already authorized source. It
// never loads Raw through a store fallback or contacts Kubernetes. Parsing is
// cached by consumer identity so many target edges do not rescan one PodSpec.
func (r *impactReferenceReader) explain(ctx context.Context, source, target graph.Resource, step ImpactStep) (ImpactReferenceEvidence, error) {
	out := ImpactReferenceEvidence{
		From: step.From, To: step.To, Type: step.Type, Status: "unknown", Uses: []ImpactReferenceUse{},
	}
	if err := r.charge(ctx, 256, step.From, step.To); err != nil {
		return out, err
	}
	if source.Kind == "Secret" {
		out.Reason = "reference_only_source"
		return out, nil
	}
	if err := r.charge(ctx, 0, string(source.UID), source.ResourceVersion); err != nil {
		return out, err
	}
	out.SourceUID, out.SourceResourceVersion = string(source.UID), source.ResourceVersion
	kind := "ConfigMap"
	switch step.Type {
	case graph.EdgeTypeUsesSecret:
		kind = "Secret"
	case graph.EdgeTypeMountsVolume:
		kind = "PersistentVolumeClaim"
	}
	// IDs omit API group. A known non-core target cannot be justified by a
	// built-in core reference with the same kind/name. Unknown target versions
	// (including reference-only Secrets) do not become instance verification.
	if target.Kind != kind || target.GroupVersion != "" && target.GroupVersion != "v1" {
		out.Reason = "target_schema_mismatch"
		return out, nil
	}
	parsed := r.sources[source.ID()]
	if parsed == nil {
		var err error
		parsed, err = r.parse(ctx, source)
		if err != nil {
			return out, err
		}
		r.sources[source.ID()] = parsed
	}
	out.Reason = parsed.reason
	if parsed.reason != "" {
		return out, nil
	}
	uses := parsed.uses[impactReferenceTarget{step.To, step.Type}]
	if len(uses) == 0 {
		out.Reason = "reference_not_found_or_not_modeled"
		return out, nil
	}
	for _, use := range uses {
		if err := r.charge(ctx, 192, use.FieldPath, use.MountFieldPath); err != nil {
			return out, err
		}
		out.Uses = append(out.Uses, use)
	}
	out.Status = "version_bound"
	return out, nil
}

// charge bounds both scanning and retained, allowlisted evidence. It does not
// measure the caller-owned graph's size or replace the future HTTP response cap.
func (r *impactReferenceReader) charge(ctx context.Context, overhead int, fields ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.entries == 0 || overhead > r.bytes {
		return ErrImpactReferenceLimit
	}
	r.entries--
	r.bytes -= overhead
	for _, field := range fields {
		if len(field) > r.bytes/6 {
			return ErrImpactReferenceLimit
		}
		r.bytes -= 6 * len(field)
	}
	return nil
}

func (r *impactReferenceReader) parse(ctx context.Context, source graph.Resource) (*impactReferenceSource, error) {
	out := &impactReferenceSource{uses: make(map[impactReferenceTarget][]ImpactReferenceUse)}
	if source.UID == "" || source.ResourceVersion == "" {
		out.reason = "missing_source_version"
		return out, nil
	}
	path := graph.ImpactPodSpecPath(source.Kind, source.GroupVersion)
	if path == nil {
		out.reason = "unsupported_source_schema"
		return out, nil
	}
	if source.Raw == nil {
		out.reason = "source_fields_unavailable"
		return out, nil
	}
	metadata, _ := source.Raw["metadata"].(map[string]any)
	if refString(source.Raw, "kind") != source.Kind || refString(source.Raw, "apiVersion") != source.GroupVersion ||
		refString(metadata, "name") != source.Name || refString(metadata, "namespace") != source.Namespace ||
		refString(metadata, "uid") != string(source.UID) || refString(metadata, "resourceVersion") != source.ResourceVersion {
		out.reason = "source_identity_or_version_mismatch"
		return out, nil
	}
	spec := source.Raw
	for _, field := range path {
		spec, _ = spec[field].(map[string]any)
	}
	if spec == nil {
		out.reason = "source_fields_unavailable"
		return out, nil
	}
	p := impactPodReferences{
		reader: r, source: source, result: out, mounts: make(map[string][]impactReferenceMount),
	}
	prefix := "/" + strings.Join(path, "/")
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		if err := p.containers(ctx, spec, field, prefix+"/"+field); err != nil {
			return nil, err
		}
	}
	if err := p.volumes(ctx, spec, prefix+"/volumes"); err != nil {
		return nil, err
	}
	// Invalid structure cannot justify any definitive default or mount binding.
	// Do not salvage fragments from a malformed source as a complete explanation.
	if p.malformed {
		out.reason = "malformed_reference_fields"
		out.uses = nil
	}
	return out, nil
}

type impactReferenceMount struct {
	path    string
	subPath bool
}

type impactPodReferences struct {
	reader    *impactReferenceReader
	source    graph.Resource
	result    *impactReferenceSource
	mounts    map[string][]impactReferenceMount
	malformed bool
}

func (p *impactPodReferences) array(object map[string]any, field string) []any {
	value, found := object[field]
	if !found {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		p.malformed = true
	}
	return items
}

func (p *impactPodReferences) object(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		p.malformed = true
	}
	return object
}

func (p *impactPodReferences) containers(ctx context.Context, spec map[string]any, field, prefix string) error {
	for i, item := range p.array(spec, field) {
		if err := p.reader.charge(ctx, 64); err != nil {
			return err
		}
		container := p.object(item)
		path := prefix + "/" + strconv.Itoa(i)
		for _, envField := range []string{"envFrom", "env"} {
			for j, entry := range p.array(container, envField) {
				if err := p.reader.charge(ctx, 64); err != nil {
					return err
				}
				object := p.object(entry)
				entryPath := path + "/" + envField + "/" + strconv.Itoa(j)
				refs := []string{"configMapRef", "secretRef"}
				if envField == "env" {
					value, present := object["valueFrom"]
					if !present {
						continue // Never inspect a literal env.value.
					}
					object = p.object(value)
					entryPath += "/valueFrom"
					refs = []string{"configMapKeyRef", "secretKeyRef"}
				}
				for k, ref := range refs {
					if value, present := object[ref]; present {
						kind := "ConfigMap"
						if k == 1 {
							kind = "Secret"
						}
						if err := p.add(ctx, p.object(value), kind, "name", entryPath+"/"+ref, "environment", "", false); err != nil {
							return err
						}
					}
				}
			}
		}
		for j, entry := range p.array(container, "volumeMounts") {
			if err := p.reader.charge(ctx, 64); err != nil {
				return err
			}
			mount := p.object(entry)
			name := refString(mount, "name")
			if name == "" {
				p.malformed = true
			}
			if err := p.reader.charge(ctx, 64, name); err != nil {
				return err
			}
			subPath := false
			for _, field := range []string{"subPath", "subPathExpr"} {
				if value, present := mount[field]; present {
					text, ok := value.(string)
					if !ok {
						p.malformed = true
					}
					subPath = subPath || text != ""
				}
			}
			p.mounts[name] = append(p.mounts[name], impactReferenceMount{path: path + "/volumeMounts/" + strconv.Itoa(j), subPath: subPath})
		}
	}
	return nil
}

func (p *impactPodReferences) volumes(ctx context.Context, spec map[string]any, prefix string) error {
	seen := make(map[string]bool)
	for i, entry := range p.array(spec, "volumes") {
		if err := p.reader.charge(ctx, 64); err != nil {
			return err
		}
		volume := p.object(entry)
		name := refString(volume, "name")
		if err := p.reader.charge(ctx, 64, name); err != nil {
			return err
		}
		if name == "" || seen[name] {
			p.malformed = true
		}
		seen[name] = true
		path := prefix + "/" + strconv.Itoa(i)
		for _, ref := range []struct{ field, kind, name string }{
			{"configMap", "ConfigMap", "name"}, {"secret", "Secret", "secretName"}, {"persistentVolumeClaim", "PersistentVolumeClaim", "claimName"},
		} {
			if value, present := volume[ref.field]; present {
				if err := p.volumeUse(ctx, p.object(value), ref.kind, ref.name, path+"/"+ref.field, name); err != nil {
					return err
				}
			}
		}
		if value, present := volume["projected"]; present {
			for j, entry := range p.array(p.object(value), "sources") {
				if err := p.reader.charge(ctx, 64); err != nil {
					return err
				}
				object := p.object(entry)
				for k, ref := range []string{"configMap", "secret"} {
					if value, present := object[ref]; present {
						kind := "ConfigMap"
						if k == 1 {
							kind = "Secret"
						}
						if err := p.volumeUse(ctx, p.object(value), kind, "name", path+"/projected/sources/"+strconv.Itoa(j)+"/"+ref, name); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

func (p *impactPodReferences) volumeUse(ctx context.Context, ref map[string]any, kind, nameField, path, volumeName string) error {
	mounts := p.mounts[volumeName]
	if len(mounts) == 0 {
		return p.add(ctx, ref, kind, nameField, path, "volume_declaration", "", false)
	}
	for _, mount := range mounts {
		if err := p.add(ctx, ref, kind, nameField, path, "volume_mount", mount.path, mount.subPath); err != nil {
			return err
		}
	}
	return nil
}

func (p *impactPodReferences) add(ctx context.Context, ref map[string]any, kind, nameField, path, mode, mountPath string, subPath bool) error {
	name := refString(ref, nameField)
	if err := p.reader.charge(ctx, 256, name, path, mountPath, p.source.Namespace, p.source.ClusterID); err != nil {
		return err
	}
	if name == "" || strings.ContainsAny(name, "/:") {
		p.malformed = true
		return nil
	}
	use := ImpactReferenceUse{FieldPath: path, MountFieldPath: mountPath, Mode: mode, Optional: "required", Refresh: "unknown"}
	if subPath {
		use.Mode = "volume_subpath"
	}
	if optional, present := ref["optional"]; present && kind != "PersistentVolumeClaim" {
		value, ok := optional.(bool)
		if !ok {
			p.malformed = true
		}
		if value {
			use.Optional = "optional"
		}
	}
	edgeType := graph.EdgeTypeUsesConfigMap
	switch kind {
	case "Secret":
		edgeType = graph.EdgeTypeUsesSecret
	case "PersistentVolumeClaim":
		edgeType = graph.EdgeTypeMountsVolume
		use.Optional, use.Refresh = "not_applicable", "storage_dependent"
	}
	if kind != "PersistentVolumeClaim" {
		switch {
		case mode == "environment":
			use.Refresh = "container_restart_required"
		case subPath:
			use.Refresh = "no_automatic_updates"
		case mode == "volume_mount":
			use.Refresh = "eventual_projection"
		}
	}
	target := graph.Resource{Kind: kind, Name: name, Namespace: p.source.Namespace, ClusterID: p.source.ClusterID}
	key := impactReferenceTarget{target.ID(), edgeType}
	p.result.uses[key] = append(p.result.uses[key], use)
	return nil
}

func refString(object map[string]any, field string) string {
	value, _ := object[field].(string)
	return value
}
