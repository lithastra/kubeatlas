// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func standaloneRepairRow(name string) graph.Resource {
	r := repairRow(name)
	r.ClusterID = ""
	return r
}

func TestStandaloneRepairScopesReceiptsEdgesAndHistory(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	missing, live := standaloneRepairRow("gone"), standaloneRepairRow("live")
	foreign, oldAPI, invalid := missing, missing, missing
	foreign.ClusterID = "east"
	oldAPI.Name, oldAPI.GroupVersion = "old-api", "other/v1"
	invalid.Name, invalid.UID = "unversioned", ""
	for _, row := range []graph.Resource{missing, live, foreign, oldAPI, invalid} {
		if err := s.UpsertResource(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, from := range []graph.Resource{missing, foreign} {
		if err := s.UpsertEdge(ctx, graph.Edge{From: from.ID(), To: live.ID(), Type: graph.EdgeTypeOwns}); err != nil {
			t.Fatal(err)
		}
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	coverage, err := tracker.Begin("", []schema.GroupVersionResource{repairPods}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	coverage.InitialDeliveryComplete(repairPods)
	coverage.RecordGap(repairPods, operations.CoveragePersistenceFailed, time.Now())
	// A different live UID still protects the same name until its watch arrives.
	live.UID = "new-uid"
	c := &coverageStubClient{resource: &coverageStubResource{list: func(_ context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
		if opts.ResourceVersion != "" || opts.Limit != memberRepairPageSize || opts.LabelSelector != "" || opts.FieldSelector != "" {
			t.Errorf("unsafe fresh-list options: %+v", opts)
		}
		return repairPage(live), nil
	}}}
	sink := &resyncSink{}
	history := NewStandaloneResourceHistory(s, sink)
	r := NewStandaloneRepair(c, s)
	for range 2 {
		r.repairOnce(ctx, repairPods, "Pod", coverage, history.RecordDelete)
	}
	if _, err := s.GetResource(ctx, missing.ID()); !errors.As(err, new(graph.ErrNotFound)) {
		t.Fatal("missing standalone row retained", err)
	}
	for _, row := range []graph.Resource{live, foreign, oldAPI, invalid} {
		if _, err := s.GetResource(ctx, row.ID()); err != nil {
			t.Fatal("repair removed present, foreign, retired or unversioned row", row, err)
		}
	}
	edges, err := s.ListEdges(ctx, live.ID(), graph.DirectionIncoming)
	if err != nil || len(edges) != 1 || edges[0].From != foreign.ID() {
		t.Fatal("wrong scoped incident-edge cascade", edges, err)
	}
	events := sink.recorded()
	if len(events) != 1 || events[0].ClusterID != "" || events[0].EventType != graph.EventTypeDelete || events[0].Data != nil || events[0].UID != string(missing.UID) {
		t.Fatal("repair did not emit exactly one unlabelled metadata-only delete", events)
	}
	evidence, err := tracker.SnapshotCoverage(ctx, "", []schema.GroupVersionResource{repairPods})
	if err != nil || len(evidence.Resources) != 1 || !slices.Contains(evidence.Resources[0].Reasons, operations.CoveragePersistenceFailed) || !slices.Contains(evidence.Resources[0].Reasons, operations.CoverageProcessingFailed) {
		t.Fatal("repair cleared existing loss or hid invalid receipts", evidence, err)
	}
}

func TestStandaloneRepairRequiresCompleteFreshSnapshot(t *testing.T) {
	for _, scenario := range []string{"complete-pages", "denied", "partial-page", "changed-snapshot", "repeated-token", "missing-snapshot", "remaining-without-token", "invalid-item", "duplicate-item", "limit", "cancelled", "no-atomic-backend", "secret", "empty-member"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := memory.New()
			missing := standaloneRepairRow("gone")
			if err := s.UpsertResource(ctx, missing); err != nil {
				t.Fatal(err)
			}
			calls := 0
			c := &coverageStubClient{resource: &coverageStubResource{list: func(_ context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
				calls++
				if opts.ResourceVersion != "" || opts.ResourceVersionMatch != "" || opts.Limit != memberRepairPageSize || opts.LabelSelector != "" || opts.FieldSelector != "" {
					t.Fatalf("unsafe list options: %+v", opts)
				}
				page := repairPage(standaloneRepairRow("live"))
				switch scenario {
				case "denied":
					return nil, errors.New("synthetic-list-denied")
				case "complete-pages", "partial-page", "changed-snapshot":
					if calls == 1 {
						page.SetContinue("next-page")
					} else {
						if opts.Continue != "next-page" {
							t.Fatal("continue token not forwarded")
						}
						page = repairPage()
						if scenario == "partial-page" {
							return nil, errors.New("synthetic-page-expired")
						}
						if scenario == "changed-snapshot" {
							page.SetResourceVersion("different-snapshot")
						}
					}
				case "repeated-token":
					page = repairPage(standaloneRepairRow(opts.Continue + "live"))
					page.SetContinue("repeated")
				case "missing-snapshot":
					page.SetResourceVersion("")
				case "remaining-without-token":
					remaining := int64(1)
					page.SetRemainingItemCount(&remaining)
				case "invalid-item":
					page.Items[0].SetUID("")
				case "duplicate-item":
					page.Items = append(page.Items, page.Items[0])
				case "limit":
					for i := 1; i <= memberRepairLimit; i++ {
						page.Items = append(page.Items, repairPage(standaloneRepairRow(time.Unix(int64(i), 0).String())).Items[0])
					}
				}
				return page, nil
			}}}
			r := NewStandaloneRepair(c, s)
			gvr, kind := repairPods, "Pod"
			switch scenario {
			case "cancelled":
				cancel()
			case "no-atomic-backend":
				r.store = struct{ graph.GraphStore }{s}
			case "secret":
				gvr.Resource, kind = "secrets", "Secret"
			case "empty-member":
				r = NewMemberRepair(c, s, "")
			}
			deletes := 0
			r.repairOnce(ctx, gvr, kind, nil, func(graph.Resource) { deletes++ })
			_, err := s.GetResource(context.Background(), missing.ID())
			if scenario == "complete-pages" {
				if calls != 2 || deletes != 1 || !errors.As(err, new(graph.ErrNotFound)) {
					t.Fatal("complete pagination did not repair", calls, deletes, err)
				}
			} else if err != nil || deletes != 0 {
				t.Fatal("invalid or partial snapshot authorized deletion", deletes, err)
			}
			if (scenario == "cancelled" || scenario == "no-atomic-backend" || scenario == "secret" || scenario == "empty-member") && calls != 0 {
				t.Fatal("invalid repair issued a list")
			}
		})
	}
}

func TestStandaloneRepairPreservesReceiptsChangedDuringList(t *testing.T) {
	for _, change := range []string{"uid", "resourceVersion", "apiVersion"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s := memory.New()
			fresh := standaloneRepairRow("same")
			if err := s.UpsertResource(ctx, fresh); err != nil {
				t.Fatal(err)
			}
			c := &coverageStubClient{resource: &coverageStubResource{list: func(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
				switch change {
				case "uid":
					fresh.UID = "replacement"
				case "resourceVersion":
					fresh.ResourceVersion = "new-opaque-rv"
				case "apiVersion":
					fresh.GroupVersion = "other/v1"
				}
				if err := s.UpsertResource(ctx, fresh); err != nil {
					t.Fatal(err)
				}
				return repairPage(), nil
			}}}
			NewStandaloneRepair(c, s).repairOnce(ctx, repairPods, "Pod", nil, func(graph.Resource) { t.Error("uncommitted deletion notified") })
			got, err := s.GetResource(ctx, fresh.ID())
			if err != nil || !graph.SameResourceVersion(got, fresh) {
				t.Fatal("in-flight listing removed changed receipt", got, err)
			}
		})
	}
}
