// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"slices"
)

// ImpactFieldShape is an immutable internal projection schema shared by the Go
// copier and PostgreSQL query compiler. It is not a Kubernetes schema validator.
// Invalid types become JSON null, never a copied payload or an omitted default.
type ImpactFieldShape struct {
	kind    string
	fields  []ImpactProjectionField
	element *ImpactFieldShape
}

type ImpactProjectionField struct {
	Name  string
	Shape *ImpactFieldShape
}

func (s *ImpactFieldShape) Kind() string               { return s.kind }
func (s *ImpactFieldShape) Element() *ImpactFieldShape { return cloneImpactShape(s.element) }
func (s *ImpactFieldShape) Fields() []ImpactProjectionField {
	fields := slices.Clone(s.fields)
	for i := range fields {
		fields[i].Shape = cloneImpactShape(fields[i].Shape)
	}
	return fields
}

func cloneImpactShape(shape *ImpactFieldShape) *ImpactFieldShape {
	if shape == nil {
		return nil
	}
	copy := *shape
	return &copy
}

// A presence string retains only whether a subPath/subPathExpr is nonempty.
// Neither its literal value nor its expression leaves the projection boundary.
const ImpactPresentString = "<present>"

var (
	impactString        = &ImpactFieldShape{kind: "string"}
	impactBool          = &ImpactFieldShape{kind: "boolean"}
	impactPresence      = &ImpactFieldShape{kind: "presence_string"}
	impactPodShape      = makeImpactReferenceShape([]string{"spec"})
	impactTemplateShape = makeImpactReferenceShape([]string{"spec", "template", "spec"})
	impactCronJobShape  = makeImpactReferenceShape([]string{"spec", "jobTemplate", "spec", "template", "spec"})
)

// ImpactPodSpecPath restricts interpretation to known built-in source schemas.
// The returned path is newly owned; callers cannot modify the shared allowlist.
func ImpactPodSpecPath(kind, gv string) []string {
	switch {
	case kind == "Pod" && gv == "v1":
		return []string{"spec"}
	case (kind == "Deployment" || kind == "ReplicaSet" || kind == "StatefulSet" || kind == "DaemonSet") && gv == "apps/v1", kind == "Job" && gv == "batch/v1":
		return []string{"spec", "template", "spec"}
	case kind == "CronJob" && gv == "batch/v1":
		return []string{"spec", "jobTemplate", "spec", "template", "spec"}
	default:
		return nil
	}
}

func ImpactReferenceShape(kind, gv string) *ImpactFieldShape {
	switch len(ImpactPodSpecPath(kind, gv)) {
	case 1:
		return cloneImpactShape(impactPodShape)
	case 3:
		return cloneImpactShape(impactTemplateShape)
	case 5:
		return cloneImpactShape(impactCronJobShape)
	default:
		return nil
	}
}

func impactObject(fields ...ImpactProjectionField) *ImpactFieldShape {
	return &ImpactFieldShape{kind: "object", fields: fields}
}

func impactArray(element *ImpactFieldShape) *ImpactFieldShape {
	return &ImpactFieldShape{kind: "array", element: element}
}

func makeImpactReferenceShape(path []string) *ImpactFieldShape {
	ref := impactObject(ImpactProjectionField{"name", impactString}, ImpactProjectionField{"optional", impactBool})
	container := impactObject(
		ImpactProjectionField{"envFrom", impactArray(impactObject(
			ImpactProjectionField{"configMapRef", ref}, ImpactProjectionField{"secretRef", ref}))},
		ImpactProjectionField{"env", impactArray(impactObject(ImpactProjectionField{"valueFrom", impactObject(
			ImpactProjectionField{"configMapKeyRef", ref}, ImpactProjectionField{"secretKeyRef", ref})}))},
		ImpactProjectionField{"volumeMounts", impactArray(impactObject(
			ImpactProjectionField{"name", impactString}, ImpactProjectionField{"subPath", impactPresence}, ImpactProjectionField{"subPathExpr", impactPresence}))},
	)
	podSpec := impactObject(
		ImpactProjectionField{"containers", impactArray(container)},
		ImpactProjectionField{"initContainers", impactArray(container)},
		ImpactProjectionField{"ephemeralContainers", impactArray(container)},
		ImpactProjectionField{"volumes", impactArray(impactObject(
			ImpactProjectionField{"name", impactString}, ImpactProjectionField{"configMap", ref},
			ImpactProjectionField{"secret", impactObject(ImpactProjectionField{"secretName", impactString}, ImpactProjectionField{"optional", impactBool})},
			ImpactProjectionField{"persistentVolumeClaim", impactObject(ImpactProjectionField{"claimName", impactString})},
			ImpactProjectionField{"projected", impactObject(ImpactProjectionField{"sources", impactArray(impactObject(
				ImpactProjectionField{"configMap", ref}, ImpactProjectionField{"secret", ref}))})},
		))},
	)
	for i := len(path) - 1; i > 0; i-- {
		podSpec = impactObject(ImpactProjectionField{path[i], podSpec})
	}
	return impactObject(
		ImpactProjectionField{"kind", impactString}, ImpactProjectionField{"apiVersion", impactString},
		ImpactProjectionField{"metadata", impactObject(
			ImpactProjectionField{"name", impactString}, ImpactProjectionField{"namespace", impactString},
			ImpactProjectionField{"uid", impactString}, ImpactProjectionField{"resourceVersion", impactString})},
		ImpactProjectionField{path[0], podSpec},
	)
}

func (b *ImpactProjectionBuilder) projectReferenceFields(ctx context.Context, r Resource) (map[string]any, error) {
	shape := ImpactReferenceShape(r.Kind, r.GroupVersion)
	if shape == nil || r.Raw == nil || r.UID == "" || r.ResourceVersion == "" {
		return nil, nil
	}
	value, err := b.projectImpactValue(ctx, shape, r.Raw)
	if err != nil {
		return nil, err
	}
	projected, _ := value.(map[string]any)
	if len(projected) == 0 {
		return nil, nil
	}
	return projected, nil
}

func (b *ImpactProjectionBuilder) projectImpactValue(ctx context.Context, shape *ImpactFieldShape, value any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.charge(32); err != nil {
		return nil, err
	}
	switch shape.kind {
	case "string":
		text, ok := value.(string)
		if !ok {
			return nil, nil
		}
		if err := b.charge(0, text); err != nil {
			return nil, err
		}
		return text, nil
	case "presence_string":
		text, ok := value.(string)
		if !ok {
			return nil, nil
		}
		if text == "" {
			return "", nil
		}
		if err := b.charge(0, ImpactPresentString); err != nil {
			return nil, err
		}
		return ImpactPresentString, nil
	case "boolean":
		flag, ok := value.(bool)
		if !ok {
			return nil, nil
		}
		return flag, nil
	case "object":
		object, ok := value.(map[string]any)
		if !ok || object == nil {
			return nil, nil
		}
		out := make(map[string]any)
		for _, field := range shape.fields {
			if entry, present := object[field.Name]; present {
				if err := b.charge(8, field.Name); err != nil {
					return nil, err
				}
				projected, err := b.projectImpactValue(ctx, field.Shape, entry)
				if err != nil {
					return nil, err
				}
				out[field.Name] = projected
			}
		}
		return out, nil
	case "array":
		items, ok := value.([]any)
		if !ok || items == nil {
			return nil, nil
		}
		out := []any{}
		for _, entry := range items {
			projected, err := b.projectImpactValue(ctx, shape.element, entry)
			if err != nil {
				return nil, err
			}
			out = append(out, projected)
		}
		return out, nil
	default:
		panic("graph: invalid internal impact projection shape")
	}
}
