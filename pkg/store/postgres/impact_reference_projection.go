// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"fmt"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// The compiler only accepts the static, immutable Go allowlist. All request
// values remain SQL parameters. No arbitrary object or container is selected
// across the wire, even if a scalar slot contains a malformed nested payload.
// JSON nulls must survive: stripping a bad optional:null would invent required.
func impactReferenceSQL() string {
	var compiler impactSQLCompiler
	var cases []string
	for _, layout := range []struct{ kind, version, condition string }{
		{"Pod", "v1", "data->>'kind' = 'Pod' AND data->>'groupVersion' = 'v1'"},
		{"Deployment", "apps/v1", "((data->>'kind' IN ('Deployment','ReplicaSet','StatefulSet','DaemonSet') AND data->>'groupVersion' = 'apps/v1') OR (data->>'kind' = 'Job' AND data->>'groupVersion' = 'batch/v1'))"},
		{"CronJob", "batch/v1", "data->>'kind' = 'CronJob' AND data->>'groupVersion' = 'batch/v1'"},
	} {
		shape := graph.ImpactReferenceShape(layout.kind, layout.version)
		cases = append(cases, "WHEN "+layout.condition+" THEN "+compiler.project(shape, "(data->'raw')"))
	}
	return "CASE WHEN COALESCE(data->>'uid', '') = '' OR COALESCE(data->>'resourceVersion', '') = '' THEN NULL ELSE CASE " + strings.Join(cases, " ") + " ELSE NULL END END"
}

type impactSQLCompiler struct{ sequence int }

func (c *impactSQLCompiler) alias() string {
	c.sequence++
	return fmt.Sprintf("impact_field_%d", c.sequence)
}

func (c *impactSQLCompiler) project(shape *graph.ImpactFieldShape, source string) string {
	condition := "jsonb_typeof(" + source + ") = "
	switch shape.Kind() {
	case "string", "boolean":
		return "CASE WHEN " + condition + impactSQLLiteral(shape.Kind()) + " THEN " + source + " ELSE 'null'::jsonb END"
	case "presence_string":
		return "CASE WHEN " + condition + "'string' THEN CASE WHEN " + source + " = '\"\"'::jsonb THEN '\"\"'::jsonb ELSE to_jsonb(" + impactSQLLiteral(graph.ImpactPresentString) + "::text) END ELSE 'null'::jsonb END"
	case "array":
		alias := c.alias()
		item := c.project(shape.Element(), alias+".value")
		return "CASE WHEN " + condition + "'array' THEN COALESCE((SELECT jsonb_agg(" + item + " ORDER BY " + alias + ".ordinality) FROM jsonb_array_elements(" + source + ") WITH ORDINALITY AS " + alias + "(value, ordinality)), '[]'::jsonb) ELSE 'null'::jsonb END"
	case "object":
		alias := c.alias()
		var fields []string
		for _, field := range shape.Fields() {
			name := impactSQLLiteral(field.Name)
			item := c.project(field.Shape, "("+source+"->"+name+")")
			fields = append(fields, "("+name+", "+source+" ? "+name+", "+item+")")
		}
		return "CASE WHEN " + condition + "'object' THEN COALESCE((SELECT jsonb_object_agg(" + alias + ".name, " + alias + ".value) FROM (VALUES " + strings.Join(fields, ", ") + ") AS " + alias + "(name, present, value) WHERE " + alias + ".present), '{}'::jsonb) ELSE 'null'::jsonb END"
	default:
		panic("postgres: invalid internal impact projection shape")
	}
}

func impactSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
