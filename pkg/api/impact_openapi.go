// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import "reflect"

// The new schema is derived only from the closed, typed response DTO (no Raw,
// maps, interfaces, or unrelated legacy schemas). This keeps every evidence
// field discoverable and v1alpha1 components byte-identical. Unsupported future
// shapes must be reviewed explicitly rather than documented as arbitrary JSON.
func impactResponseSchema() map[string]any { return impactSchemaFor(reflect.TypeFor[ImpactResponse]()) }

func impactSchemaFor(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		s := impactSchemaFor(t.Elem())
		s["nullable"] = true
		return s
	}
	if t == impactTimeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Slice:
		return map[string]any{"type": "array", "nullable": true, "items": impactSchemaFor(t.Elem())}
	case reflect.Struct:
		fields, err := impactJSONFields(t)
		if err != nil {
			panic("invalid static impact response schema")
		}
		props := make(map[string]any, len(fields))
		required := []string{}
		for _, field := range fields {
			props[field.name] = impactSchemaFor(field.typ)
			if !field.omitEmpty {
				required = append(required, field.name)
			}
		}
		return map[string]any{"type": "object", "properties": props, "required": required}
	default:
		panic("unsupported static impact response schema")
	}
}
