// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestDiffPaths_DetectsRemoval(t *testing.T) {
	base := map[string]any{
		"paths": map[string]any{
			"/api/v1alpha1/graph": map[string]any{"get": map[string]any{}},
		},
	}
	cur := map[string]any{"paths": map[string]any{}}
	got := diffPaths(base, cur)
	if len(got) != 1 || got[0] != "path removed: /api/v1alpha1/graph" {
		t.Errorf("diffPaths missed removal, got %v", got)
	}
}

func TestDiffPaths_AdditionIsFine(t *testing.T) {
	base := map[string]any{"paths": map[string]any{}}
	cur := map[string]any{
		"paths": map[string]any{
			"/api/v1alpha1/graph": map[string]any{"get": map[string]any{}},
		},
	}
	if got := diffPaths(base, cur); len(got) != 0 {
		t.Errorf("addition flagged as break: %v", got)
	}
}

func TestDiffSchemas_DetectsPropertyRemoval(t *testing.T) {
	base := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"Resource": map[string]any{
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	cur := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"Resource": map[string]any{
					"properties": map[string]any{},
				},
			},
		},
	}
	got := diffSchemas(base, cur)
	if len(got) != 1 || got[0] != "property removed: Resource.name" {
		t.Errorf("diffSchemas missed property removal, got %v", got)
	}
}

func TestDiffSchemas_DetectsTypeChange(t *testing.T) {
	base := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"X": map[string]any{
					"properties": map[string]any{
						"f": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	cur := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"X": map[string]any{
					"properties": map[string]any{
						"f": map[string]any{"type": "integer"},
					},
				},
			},
		},
	}
	got := diffSchemas(base, cur)
	if len(got) != 1 || got[0] != "property type changed: X.f string -> integer" {
		t.Errorf("diffSchemas missed type change, got %v", got)
	}
}

func TestDiffSchemas_NewRequiredIsBreak(t *testing.T) {
	base := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"X": map[string]any{
					"properties": map[string]any{"f": map[string]any{"type": "string"}},
				},
			},
		},
	}
	cur := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"X": map[string]any{
					"properties": map[string]any{"f": map[string]any{"type": "string"}},
					"required":   []any{"f"},
				},
			},
		},
	}
	got := diffSchemas(base, cur)
	if len(got) != 1 || got[0] != "new required field added: X.f" {
		t.Errorf("diffSchemas missed new-required, got %v", got)
	}
}

func TestDiffSchemas_AdditionIsFine(t *testing.T) {
	base := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"X": map[string]any{
					"properties": map[string]any{"a": map[string]any{"type": "string"}},
				},
			},
		},
	}
	cur := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"X": map[string]any{
					"properties": map[string]any{
						"a": map[string]any{"type": "string"},
						"b": map[string]any{"type": "boolean"},
					},
				},
				"Y": map[string]any{}, // brand new schema
			},
		},
	}
	if got := diffSchemas(base, cur); len(got) != 0 {
		t.Errorf("addition flagged as break: %v", got)
	}
}
