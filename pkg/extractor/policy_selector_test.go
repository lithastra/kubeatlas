// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func policySelectorFixture(mode string, selector any) (graph.Resource, Extractor) {
	if mode == "gatekeeper" {
		return gkConstraint("policy", map[string]any{"kinds": []any{map[string]any{"kinds": []any{"Pod"}}}, "labelSelector": selector}, nil), GatekeeperExtractor{}
	}
	parts := strings.Split(mode, "/")
	rule := map[string]any{"validate": map[string]any{}, "match": map[string]any{parts[1]: []any{map[string]any{"resources": map[string]any{"kinds": []any{"Pod"}, "selector": selector}}}}}
	return kyvernoPolicy(parts[0], "policy", "demo", []any{rule}), KyvernoExtractor{}
}

func TestPolicyMalformedLabelSelectorsRejectTargetReads(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, mode := range []string{"gatekeeper", "ClusterPolicy/any", "ClusterPolicy/all", "Policy/any", "Policy/all"} {
			for _, tc := range []struct {
				name  string
				value any
			}{
				{"scalar", "policy-selector-canary"},
				{"array", []any{"policy-selector-canary"}},
				{"map-scalar", map[string]any{"matchLabels": "policy-selector-canary"}},
				{"boolean", map[string]any{"matchLabels": map[string]any{"flag": true}}},
				{"mixed", map[string]any{"matchLabels": map[string]any{"app": "web", "private-key-canary": int64(7)}}},
				{"nested", map[string]any{"matchLabels": map[string]any{"app": "web", "private-key-canary": map[string]any{"value": "policy-selector-canary"}}}},
			} {
				t.Run(fmt.Sprintf("cluster=%s/%s/%s", cluster, mode, tc.name), func(t *testing.T) {
					r, ext := policySelectorFixture(mode, tc.value)
					r.ClusterID = cluster
					var reads []string
					q := kyvernoFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
						reads = append(reads, f.Kind)
						return nil, nil
					})
					edges, err := ext.Extract(context.Background(), r, q)
					var wantReads []string
					if mode != "gatekeeper" {
						wantReads = []string{"PolicyReport", "ClusterPolicyReport"}
					}
					if err == nil || len(edges) != 0 || !reflect.DeepEqual(reads, wantReads) {
						t.Fatal("malformed policy selector became a target query", edges, reads, err)
					}
					if strings.Contains(err.Error(), "policy-selector-canary") || strings.Contains(err.Error(), "private-key-canary") {
						t.Fatal("policy selector error included input data", err)
					}
				})
			}
		}
	}
}

func TestPolicyLabelSelectorEmptyAndEqualityCompatibility(t *testing.T) {
	for _, mode := range []string{"gatekeeper", "ClusterPolicy/any", "ClusterPolicy/all", "Policy/any", "Policy/all"} {
		for _, tc := range []struct {
			name     string
			selector any
			want     []string
		}{
			{"null", nil, []string{"demo/Pod/empty", "demo/Pod/absent", "demo/Pod/value"}},
			{"empty", map[string]any{}, []string{"demo/Pod/empty", "demo/Pod/absent", "demo/Pod/value"}},
			{"null-labels", map[string]any{"matchLabels": nil}, []string{"demo/Pod/empty", "demo/Pod/absent", "demo/Pod/value"}},
			{"empty-value", map[string]any{"matchLabels": map[string]any{"flag": ""}}, []string{"demo/Pod/empty"}},
			{"conjunction", map[string]any{"matchLabels": map[string]any{"flag": "", "app": "web"}}, []string{"demo/Pod/empty"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				r, ext := policySelectorFixture(mode, tc.selector)
				rows := sliceLister{
					{Kind: "Pod", Namespace: "demo", Name: "empty", Labels: map[string]string{"flag": "", "app": "web"}},
					{Kind: "Pod", Namespace: "demo", Name: "absent", Labels: map[string]string{"app": "web"}},
					{Kind: "Pod", Namespace: "demo", Name: "value", Labels: map[string]string{"flag": "value", "app": "web"}},
				}
				edges, err := ext.Extract(context.Background(), r, rows)
				var ids []string
				for _, e := range edges {
					if e.From != r.ID() || e.Type != graph.EdgeTypeEnforces {
						t.Fatal("selector changed policy edge identity", e)
					}
					ids = append(ids, e.To)
				}
				if err != nil || !reflect.DeepEqual(ids, tc.want) {
					t.Fatal("selector validation changed empty/equality behavior", ids, tc.want, err)
				}
			})
		}
	}
}

func TestKyvernoMalformedSelectorPreservesPrefixAndErrors(t *testing.T) {
	for _, kind := range []string{"Policy", "ClusterPolicy"} {
		for _, placement := range []string{"any", "all", "any-to-all", "later-rule"} {
			for _, failed := range []string{"none", "report", "target", "cancel"} {
				t.Run(kind+"/"+placement+"/"+failed, func(t *testing.T) {
					good := map[string]any{"resources": map[string]any{"kinds": []any{"Pod"}, "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}}
					bad := map[string]any{"resources": map[string]any{"kinds": []any{"Deployment"}, "selector": map[string]any{"matchLabels": map[string]any{"app": "web", "invalid": true}}}}
					later := map[string]any{"resources": map[string]any{"kinds": []any{"Namespace"}}}
					match := map[string]any{"any": []any{good, bad, later}}
					switch placement {
					case "all":
						match = map[string]any{"all": []any{good, bad, later}}
					case "any-to-all":
						match = map[string]any{"any": []any{good}, "all": []any{bad, later}}
					}
					rules := []any{map[string]any{"validate": map[string]any{}, "match": match}, validateRule([]any{"Service"}, nil)}
					if placement == "later-rule" {
						rules = []any{map[string]any{"validate": map[string]any{}, "match": map[string]any{"any": []any{good}}}, map[string]any{"validate": map[string]any{}, "match": map[string]any{"any": []any{bad, later}}}, validateRule([]any{"Service"}, nil)}
					}
					r := kyvernoPolicy(kind, "policy", "demo", rules)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					reportErr, targetErr := errors.New("report failure"), errors.New("target failure")
					var reads []string
					q := kyvernoFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
						reads = append(reads, f.Kind)
						if f.Kind == "PolicyReport" && failed == "report" {
							return nil, reportErr
						}
						if f.Kind == "Pod" {
							row := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "confirmed"}
							if failed == "target" {
								return []graph.Resource{row}, targetErr
							}
							if failed == "cancel" {
								cancel()
							}
							return []graph.Resource{row}, nil
						}
						return nil, nil
					})
					edges, err := (KyvernoExtractor{}).Extract(ctx, r, q)
					want := 1
					if failed == "target" || failed == "cancel" {
						want = 0
					}
					if err == nil || len(edges) != want || !reflect.DeepEqual(reads, []string{"PolicyReport", "ClusterPolicyReport", "Pod"}) || errors.Is(err, reportErr) != (failed == "report") || errors.Is(err, targetErr) != (failed == "target") || errors.Is(err, context.Canceled) != (failed == "cancel") {
						t.Fatal("malformed descriptor lost prefix/error or continued target reads", edges, reads, err)
					}
					if len(edges) == 1 && (edges[0].From != r.ID() || edges[0].To != "demo/Pod/confirmed" || edges[0].Type != graph.EdgeTypeEnforces) {
						t.Fatal("prefix changed its policy edge", edges)
					}
				})
			}
		}
	}
}
