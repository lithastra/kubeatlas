// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lithastra/kubeatlas/pkg/extractor/rego"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// runRulesTest is the `kubeatlas rules-test` subcommand: load a
// rule pack from a local directory or an OCI ref, evaluate every
// YAML under --samples through the engine, and report which edges
// each sample produced. Designed for rule-pack contributors who
// want to validate locally without spinning up a cluster.
//
// Exit codes:
//
//	0 — every sample produced at least one edge AND no sample
//	    errored.
//	1 — usage / load error.
//	2 — at least one sample produced zero edges OR errored;
//	    the per-sample report is written to stdout regardless.
//
// Output is human-readable by default (table-shaped); --format=json
// emits a single JSON object whose `samples` array carries one
// entry per fixture.
func runRulesTest(args []string) int {
	fs := flag.NewFlagSet("rules-test", flag.ContinueOnError)
	pack := fs.String("pack", "",
		"Rule pack to load. Either a local directory or an OCI ref "+
			"(oci://ghcr.io/lithastra/rules/<pack>:<version>).")
	samples := fs.String("samples", "",
		"Directory containing YAML samples to evaluate. "+
			"Defaults to <pack>/samples when --pack is a directory.")
	format := fs.String("format", "text",
		"Output format: text | json.")

	if err := fs.Parse(args); err != nil {
		// flag prints its own error message; just propagate the code.
		return 1
	}
	if *pack == "" {
		fmt.Fprintln(os.Stderr, "rules-test: --pack is required")
		fs.Usage()
		return 1
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(os.Stderr, "rules-test: --format must be text or json")
		return 1
	}

	rp, err := loadPackForRulesTest(*pack)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rules-test:", err)
		return 1
	}
	resolvedSamples := *samples
	if resolvedSamples == "" {
		if !strings.HasPrefix(*pack, "oci://") && !looksLikeOCIRef(*pack) {
			resolvedSamples = filepath.Join(*pack, "samples")
		} else {
			fmt.Fprintln(os.Stderr,
				"rules-test: --samples required when --pack is an OCI ref")
			return 1
		}
	}
	if _, err := os.Stat(resolvedSamples); err != nil {
		fmt.Fprintf(os.Stderr, "rules-test: samples dir %q not readable: %v\n",
			resolvedSamples, err)
		return 1
	}

	engine := rego.New()
	if err := rp.RegisterTo(context.Background(), engine); err != nil {
		fmt.Fprintln(os.Stderr, "rules-test: register pack:", err)
		return 1
	}

	results, err := evaluateSamples(rp, engine, resolvedSamples)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rules-test:", err)
		return 1
	}

	report := rulesTestReport{
		Pack:    rp.Name,
		Version: rp.Version,
		Samples: results,
	}

	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		report.printText(os.Stdout)
	}

	for _, s := range results {
		if s.Error != "" || len(s.Edges) == 0 {
			return 2
		}
	}
	return 0
}

// loadPackForRulesTest dispatches to the right loader based on ref
// shape. OCI refs flow through LoadRulePackFromOCI; bare paths
// through LoadRulePackFromDir.
func loadPackForRulesTest(ref string) (*rego.RulePack, error) {
	if strings.HasPrefix(ref, "oci://") || looksLikeOCIRef(ref) {
		return rego.LoadRulePackFromOCI(context.Background(), ref)
	}
	return rego.LoadRulePackFromDir(ref)
}

// looksLikeOCIRef heuristically tells "ghcr.io/foo/bar:0.1.0" from
// a Windows-style "C:\path\pack" — we run on linux/darwin only, so
// a colon plus a slash means OCI. Bare relative or absolute paths
// stay on the directory loader path.
func looksLikeOCIRef(s string) bool {
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "/") {
		return false
	}
	return strings.Contains(s, ":") && strings.Contains(s, "/")
}

// rulesTestReport is what --format=json emits.
type rulesTestReport struct {
	Pack    string         `json:"pack"`
	Version string         `json:"version"`
	Samples []sampleResult `json:"samples"`
}

type sampleResult struct {
	File  string       `json:"file"`
	Edges []graph.Edge `json:"edges"`
	Error string       `json:"error,omitempty"`
}

func (r rulesTestReport) printText(w *os.File) {
	_, _ = fmt.Fprintf(w, "rule pack: %s v%s\n", r.Pack, r.Version)
	_, _ = fmt.Fprintf(w, "samples:   %d\n\n", len(r.Samples))
	for _, s := range r.Samples {
		switch {
		case s.Error != "":
			_, _ = fmt.Fprintf(w, "  ✗ %s — %s\n", s.File, s.Error)
		case len(s.Edges) == 0:
			_, _ = fmt.Fprintf(w, "  ✗ %s — no edges produced\n", s.File)
		default:
			_, _ = fmt.Fprintf(w, "  ✓ %s — %d edge(s)\n", s.File, len(s.Edges))
			for _, e := range s.Edges {
				_, _ = fmt.Fprintf(w, "      %-20s %s -> %s\n", e.Type, e.From, e.To)
			}
		}
	}
}

// evaluateSamples walks dir, parses every *.yaml / *.yml as an
// unstructured K8s object, runs it through the engine's built-in
// router/cache (we wire a lightweight one here for the CLI), and
// records the derived edges.
func evaluateSamples(rp *rego.RulePack, engine *rego.Engine, dir string) ([]sampleResult, error) {
	var out []sampleResult

	router := rego.FromRulePacks(rp)
	cache, err := rego.NewCache(0, rego.NewMetrics())
	if err != nil {
		return nil, fmt.Errorf("build cache: %w", err)
	}
	// Re-wire the engine with router + cache so EvaluateForResource
	// works. Engine accepts these as Options, but we already built it
	// without them; the cleanest path is to rebuild here.
	rebuilt := rego.New(
		rego.WithRouter(router),
		rego.WithCache(cache),
	)
	if err := rp.RegisterTo(context.Background(), rebuilt); err != nil {
		return nil, fmt.Errorf("re-register pack: %w", err)
	}

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".yaml") && !strings.HasSuffix(d.Name(), ".yml") {
			return nil
		}

		body, readErr := os.ReadFile(path)
		if readErr != nil {
			out = append(out, sampleResult{File: path, Error: readErr.Error()})
			return nil
		}
		resource, decodeErr := yamlToResource(body)
		if decodeErr != nil {
			out = append(out, sampleResult{File: path, Error: decodeErr.Error()})
			return nil
		}

		edges, evalErr := rebuilt.EvaluateForResource(context.Background(), resource)
		if evalErr != nil {
			out = append(out, sampleResult{File: path, Error: evalErr.Error()})
			return nil
		}
		out = append(out, sampleResult{File: path, Edges: edges})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if len(out) == 0 {
		return nil, errors.New("no .yaml / .yml samples found")
	}
	return out, nil
}

// yamlToResource maps a single-document Kubernetes YAML manifest
// into a graph.Resource the engine can route. Only the metadata
// fields the rule-pack input contract documents are populated; full
// unstructured payload lands in Resource.Raw so rules that read
// spec.* paths still work.
func yamlToResource(body []byte) (graph.Resource, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return graph.Resource{}, fmt.Errorf("yaml: %w", err)
	}
	kind, _ := raw["kind"].(string)
	if kind == "" {
		return graph.Resource{}, errors.New("missing or non-string .kind")
	}
	apiVersion, _ := raw["apiVersion"].(string)

	var (
		ns, name, uid string
		labels        map[string]string
	)
	if md, ok := raw["metadata"].(map[string]any); ok {
		ns, _ = md["namespace"].(string)
		name, _ = md["name"].(string)
		uid, _ = md["uid"].(string)
		if rawLabels, ok := md["labels"].(map[string]any); ok {
			labels = make(map[string]string, len(rawLabels))
			for k, v := range rawLabels {
				if s, ok := v.(string); ok {
					labels[k] = s
				}
			}
		}
	}
	if name == "" {
		return graph.Resource{}, errors.New("missing or non-string .metadata.name")
	}
	// Sample YAML rarely carries metadata.uid. Synthesize a stable one
	// from kind/namespace/name so two fixtures still get distinct
	// engine cache keys (CacheKey is UID + resourceVersion + ruleHash):
	// without this, two samples that route through the SAME .rego file
	// — e.g. a pack that registers one module file under two GVK
	// matches — would collide on the empty UID and the second sample
	// would wrongly read the first's cached edges.
	if uid == "" {
		uid = kind + "/" + ns + "/" + name
	}

	return graph.Resource{
		Kind:         kind,
		Name:         name,
		Namespace:    ns,
		UID:          types.UID(uid),
		Labels:       labels,
		GroupVersion: apiVersion,
		Raw:          raw,
	}, nil
}
