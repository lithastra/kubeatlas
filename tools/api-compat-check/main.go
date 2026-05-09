// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Command api-compat-check enforces the v1alpha1 surface contract:
// no path may be removed, no field may be removed or renamed, and
// no scalar type may change. CI runs this against every PR with
// the baseline being the v0.1.0 spec saved at
// api/openapi-v1alpha1.json.
//
// Usage:
//
//	api-compat-check --baseline=api/openapi-v1alpha1.json \
//	                 --current=api/openapi-v1alpha1.current.json
//
// Exit codes:
//
//	0 — current spec is a superset of baseline.
//	2 — at least one breaking change was detected; report on stderr.
//
// The tool intentionally does NOT block additions (new endpoints,
// new optional fields). v1alpha1 is frozen on removals only —
// adding a never-required field is safe.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	baseline := flag.String("baseline", "",
		"Path to the v0.1.0 OpenAPI spec used as the no-break floor.")
	current := flag.String("current", "",
		"Path to the OpenAPI spec emitted by the current build.")
	flag.Parse()

	if *baseline == "" || *current == "" {
		fmt.Fprintln(os.Stderr,
			"api-compat-check: --baseline and --current are both required")
		os.Exit(1)
	}

	base, err := loadSpec(*baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-compat-check: load %s: %v\n", *baseline, err)
		os.Exit(1)
	}
	cur, err := loadSpec(*current)
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-compat-check: load %s: %v\n", *current, err)
		os.Exit(1)
	}

	var breaks []string
	breaks = append(breaks, diffPaths(base, cur)...)
	breaks = append(breaks, diffSchemas(base, cur)...)

	if len(breaks) == 0 {
		fmt.Println("api-compat-check: OK (no breaking changes detected)")
		return
	}
	sort.Strings(breaks)
	fmt.Fprintln(os.Stderr, "api-compat-check: breaking changes detected:")
	for _, b := range breaks {
		fmt.Fprintf(os.Stderr, "  - %s\n", b)
	}
	os.Exit(2)
}

func loadSpec(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return doc, nil
}

// diffPaths returns one issue per missing path or method. Adding
// a new path is fine; adding a new method on an existing path is
// fine; removing either is a break.
func diffPaths(base, cur map[string]any) []string {
	basePaths, _ := base["paths"].(map[string]any)
	curPaths, _ := cur["paths"].(map[string]any)
	var out []string
	for path, ops := range basePaths {
		curOps, ok := curPaths[path].(map[string]any)
		if !ok {
			out = append(out, fmt.Sprintf("path removed: %s", path))
			continue
		}
		for method := range ops.(map[string]any) {
			if _, ok := curOps[method]; !ok {
				out = append(out, fmt.Sprintf("method removed: %s %s",
					strings.ToUpper(method), path))
			}
		}
	}
	return out
}

// diffSchemas walks every component schema in the baseline and
// asserts the current spec carries the same schema with the same
// (or wider) properties. New schemas in current are fine; new
// optional properties on an existing schema are fine; type
// changes / property removals are breaks.
func diffSchemas(base, cur map[string]any) []string {
	baseComp, _ := base["components"].(map[string]any)
	curComp, _ := cur["components"].(map[string]any)
	baseSchemas, _ := baseComp["schemas"].(map[string]any)
	curSchemas, _ := curComp["schemas"].(map[string]any)

	var out []string
	for name, raw := range baseSchemas {
		baseSchema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		curRaw, ok := curSchemas[name]
		if !ok {
			out = append(out, fmt.Sprintf("schema removed: %s", name))
			continue
		}
		curSchema, ok := curRaw.(map[string]any)
		if !ok {
			out = append(out, fmt.Sprintf("schema malformed in current: %s", name))
			continue
		}
		out = append(out, diffSchemaShape(name, baseSchema, curSchema)...)
	}
	return out
}

// diffSchemaShape compares one schema's properties + required
// list. Property removal, type change on an existing property,
// and "required in baseline but not in current" are breaks; the
// inverse (newly required field in current) is also a break for
// any client that pre-dated the change, so we flag it too.
func diffSchemaShape(name string, base, cur map[string]any) []string {
	var out []string

	baseProps, _ := base["properties"].(map[string]any)
	curProps, _ := cur["properties"].(map[string]any)
	for pname, praw := range baseProps {
		bp, ok := praw.(map[string]any)
		if !ok {
			continue
		}
		cpRaw, ok := curProps[pname]
		if !ok {
			out = append(out, fmt.Sprintf("property removed: %s.%s", name, pname))
			continue
		}
		cp, ok := cpRaw.(map[string]any)
		if !ok {
			continue
		}
		bt, _ := bp["type"].(string)
		ct, _ := cp["type"].(string)
		if bt != "" && ct != "" && bt != ct {
			out = append(out,
				fmt.Sprintf("property type changed: %s.%s %s -> %s", name, pname, bt, ct))
		}
	}

	baseReq := stringSet(base["required"])
	curReq := stringSet(cur["required"])
	for r := range baseReq {
		if !curReq[r] {
			out = append(out,
				fmt.Sprintf("required field demoted to optional: %s.%s", name, r))
		}
	}
	// Newly required fields are equally breaking: a v1alpha1 client
	// that pre-dated the change won't know to send them.
	for r := range curReq {
		if !baseReq[r] {
			out = append(out,
				fmt.Sprintf("new required field added: %s.%s", name, r))
		}
	}
	return out
}

func stringSet(v any) map[string]bool {
	out := map[string]bool{}
	arr, ok := v.([]any)
	if !ok {
		return out
	}
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out[s] = true
		}
	}
	return out
}
