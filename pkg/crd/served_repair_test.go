// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"
)

type servedHistorySink struct {
	mu     sync.Mutex
	events []graph.ResourceEvent
}

func (s *servedHistorySink) Enqueue(event graph.ResourceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func servedRow(version, name string) graph.Resource {
	return graph.Resource{Namespace: "default", Kind: "Foo", Name: name, GroupVersion: "example.com/" + version, UID: types.UID("uid-" + name), ResourceVersion: "old-rv"}
}

func TestCRDServedVersionRepairDoesNotInferCrossVersionAbsence(t *testing.T) {
	for _, mode := range []string{"history-disabled", "history-enabled", "old-endpoint-missing"} {
		t.Run(mode, func(t *testing.T) {
			s := memory.New()
			meta, live := coverageObjects(t)
			meta = mutateCRD(t, meta, selectV2)
			live.SetUID("uid-live")
			live.SetName("live-only-in-v1")
			d, client, tracker, ctx := reconciliationDiscovery(t, s, live)
			d.memberRepair = discovery.NewStandaloneRepair(client, s)
			sink := &servedHistorySink{}
			if mode != "history-disabled" {
				d.resourceHistory = discovery.NewStandaloneResourceHistory(s, sink)
			}
			if mode == "old-endpoint-missing" {
				client.PrependReactor("list", "foos", func(action clienttesting.Action) (bool, runtime.Object, error) {
					if action.GetResource() == coverageFooGVR {
						return true, nil, apierrors.NewNotFound(coverageFooGVR.GroupResource(), "")
					}
					return false, nil, nil
				})
			}
			old, current := servedRow("v1", "old-gone"), servedRow("v2", "current-gone")
			oldLive, unserved, foreign := servedRow("v1", live.GetName()), servedRow("v0", "unserved"), old
			foreign.ClusterID = "east"
			for _, row := range []graph.Resource{old, current, oldLive, unserved, foreign} {
				if err := s.UpsertResource(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.UpsertEdge(ctx, graph.Edge{From: old.ID(), To: oldLive.ID(), Type: graph.EdgeTypeOwns}); err != nil {
				t.Fatal(err)
			}
			d.onCRDAdd(ctx, meta)
			waitFor(t, 3*time.Second, "served-version repair round", func() bool {
				row := coverageRow(crdCoverage(t, tracker), reconciliationV2)
				if mode == "old-endpoint-missing" {
					return slices.Contains(row.Reasons, operations.CoverageProcessingFailed)
				}
				_, oldErr := s.GetResource(ctx, old.ID())
				_, currentErr := s.GetResource(ctx, current.ID())
				return errors.As(oldErr, new(graph.ErrNotFound)) && errors.As(currentErr, new(graph.ErrNotFound)) && row.InitialDeliveryDone
			})
			entry := reconciliationEntry(d, meta.GetName())
			for range 3 {
				d.onCRDAdd(ctx, meta.DeepCopy())
			}
			if reconciliationEntry(d, meta.GetName()) != entry {
				t.Fatal("metadata replay restarted selected observation")
			}
			d.shutdown() // Joins repairs before checking final receipts.
			for _, row := range []graph.Resource{oldLive, unserved, foreign} {
				if _, err := s.GetResource(ctx, row.ID()); err != nil {
					t.Fatal("selected endpoint absence swept protected row", row, err)
				}
			}
			sink.mu.Lock()
			events := slices.Clone(sink.events)
			sink.mu.Unlock()
			if mode == "old-endpoint-missing" {
				for _, row := range []graph.Resource{old, current} {
					if _, err := s.GetResource(ctx, row.ID()); err != nil {
						t.Fatal("404 authorized deletion", row, err)
					}
				}
				if len(events) != 0 {
					t.Fatal("failed endpoint fabricated delete history", events)
				}
			} else {
				edges, err := s.ListEdges(ctx, oldLive.ID(), graph.DirectionIncoming)
				if err != nil || len(edges) != 0 {
					t.Fatal("committed repair did not cascade incident edges", edges, err)
				}
				want := 0
				if mode == "history-enabled" {
					want = 2
				}
				if len(events) != want {
					t.Fatal("repair event count", len(events), want)
				}
				for _, event := range events {
					if event.ClusterID != "" || event.EventType != graph.EventTypeDelete || event.Data != nil {
						t.Fatal("invalid repair history receipt", event)
					}
				}
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "watch" && action.GetResource() != reconciliationV2 {
					t.Fatal("repair created an additional version watch", action)
				}
				if action.GetVerb() != "list" && action.GetVerb() != "watch" {
					t.Fatal("repair wrote to Kubernetes", action)
				}
			}
		})
	}
}

func TestCRDServedVersionRevocationCancelsRoundWithoutRestartingWatch(t *testing.T) {
	s := memory.New()
	d, client, tracker, ctx := reconciliationDiscovery(t, s)
	d.memberRepair = discovery.NewStandaloneRepair(client, s)
	old := servedRow("v1", "retained")
	if err := s.UpsertResource(ctx, old); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	client.PrependReactor("list", "foos", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != coverageFooGVR {
			return false, nil, nil
		}
		close(entered)
		<-release // Deliberately ignores context, like an already in-flight read.
		page := &unstructured.UnstructuredList{}
		page.SetResourceVersion("complete-but-revoked")
		return true, page, nil
	})
	meta, _ := coverageObjects(t)
	meta = mutateCRD(t, meta, selectV2)
	d.onCRDAdd(ctx, meta)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old-version list did not start")
	}
	entry := reconciliationEntry(d, meta.GetName())
	d.mu.Lock()
	oldLifetime := entry.versionCtx
	d.mu.Unlock()
	before := coverageRow(crdCoverage(t, tracker), reconciliationV2)
	updated := mutateCRD(t, meta, func(crd *apiextensionsv1.CustomResourceDefinition) {
		crd.Spec.Versions[0].Served = false
	})
	d.onCRDAdd(ctx, updated)
	d.mu.Lock()
	unchanged := d.informers[meta.GetName()] == entry
	versions := slices.Clone(entry.versions)
	newLifetime := entry.versionCtx
	d.mu.Unlock()
	if !unchanged || !slices.Equal(versions, []string{"v2"}) || !errors.Is(oldLifetime.Err(), context.Canceled) || newLifetime.Err() != nil {
		t.Fatal("served-set update did not revoke only its repair lifetime", unchanged, versions)
	}
	if after := coverageRow(crdCoverage(t, tracker), reconciliationV2); after.ContinuityRevision < before.ContinuityRevision || after.Since != before.Since {
		t.Fatal("served-set update reset selected watch evidence", before, after)
	}
	unblock()
	d.shutdown()
	if got, err := s.GetResource(ctx, old.ID()); err != nil || !graph.SameResourceVersion(got, old) {
		t.Fatal("revoked old-version round removed retained row", got, err)
	}
}

func TestCRDServedVersionRepairRetainsRowsFromDifferentScope(t *testing.T) {
	for _, scope := range []apiextensionsv1.ResourceScope{apiextensionsv1.ClusterScoped, apiextensionsv1.NamespaceScoped} {
		t.Run(string(scope), func(t *testing.T) {
			s := memory.New()
			d, client, tracker, ctx := reconciliationDiscovery(t, s)
			d.memberRepair = discovery.NewStandaloneRepair(client, s)
			old, active := servedRow("v1", "retired-scope"), servedRow("v1", "active-scope")
			if scope == apiextensionsv1.NamespaceScoped {
				old.Namespace = ""
			} else {
				active.Namespace = ""
			}
			for _, row := range []graph.Resource{old, active} {
				if err := s.UpsertResource(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			meta, _ := coverageObjects(t)
			meta = mutateCRD(t, meta, func(crd *apiextensionsv1.CustomResourceDefinition) {
				selectV2(crd)
				crd.Spec.Scope = scope
			})
			d.onCRDAdd(ctx, meta)
			waitFor(t, 3*time.Second, "only current-scope retained row repaired", func() bool {
				_, err := s.GetResource(ctx, active.ID())
				return errors.As(err, new(graph.ErrNotFound))
			})
			d.shutdown()
			if _, err := s.GetResource(ctx, old.ID()); err != nil {
				t.Fatal("repair inferred absence across resource scopes", err)
			}
			row := coverageRow(crdCoverage(t, tracker), reconciliationV2)
			if !slices.Contains(row.Reasons, operations.CoverageProcessingFailed) {
				t.Fatal("retained old scope hidden from coverage qualification", row)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "watch" && action.GetResource() != reconciliationV2 {
					t.Fatal("repair created an old-scope watch", action)
				}
			}
		})
	}
}
