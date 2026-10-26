// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

func templateIdentityRows(mode string) []metav1.APIResource {
	row := metav1.APIResource{Name: "constrainttemplates", Kind: "ConstraintTemplate", Verbs: metav1.Verbs{"list", "watch"}}
	rows := []metav1.APIResource{row}
	switch mode {
	case "explicit":
		rows[0].Group, rows[0].Version = "templates.gatekeeper.sh", "v1"
	case "wrong-row-group":
		rows[0].Group = "foreign-canary.example"
	case "wrong-row-version":
		rows[0].Version = "v1beta1"
	case "duplicate":
		rows = append(rows, row)
	case "conflicting-duplicate":
		bad := row
		bad.Verbs = metav1.Verbs{"list"}
		rows = append(rows, bad)
	case "reverse-conflict":
		bad := row
		bad.Namespaced = true
		rows = []metav1.APIResource{bad, row}
	case "late-duplicate":
		rows = append(rows, metav1.APIResource{Name: "other", Kind: "Unrelated"}, row)
	case "subresource":
		rows = append(rows, metav1.APIResource{Name: "constrainttemplates/status", Kind: "ConstraintTemplate"})
	case "unrelated-malformed":
		rows = append(rows, metav1.APIResource{Name: "other", Kind: "Invalid/Unrelated"})
	}
	return rows
}

func supportedTemplateIdentity(mode string) bool {
	return slices.Contains([]string{"implicit", "explicit", "subresource", "unrelated-malformed"}, mode)
}

func TestTemplateDiscoveryRequiresOneConsistentTarget(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, mode := range []string{"implicit", "explicit", "wrong-row-group", "wrong-row-version", "duplicate", "conflicting-duplicate", "reverse-conflict", "late-duplicate", "subresource", "unrelated-malformed"} {
			t.Run(fmt.Sprintf("cluster=%s/%s", cluster, mode), func(t *testing.T) {
				tracker := operations.NewCoverageTracker(time.Minute)
				source, err := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				token, err := source.BeginType(coverageConstraintGVR, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				prior := time.Now().Add(-time.Minute)
				token.RecordGap(coverageConstraintGVR, operations.CoveragePersistenceFailed, prior)
				calls := 0
				active := mode
				d := newAwaitDiscovery(templateDiscovery{read: func(ctx context.Context, version string) (*metav1.APIResourceList, error) {
					calls++
					if deadline, ok := ctx.Deadline(); version != gkGV() || !ok || time.Until(deadline) > 5*time.Second {
						t.Fatal("template identity check changed discovery scope/budget", version)
					}
					return &metav1.APIResourceList{GroupVersion: version, APIResources: templateIdentityRows(active)}, nil
				}}, time.Hour)
				d.clusterID, d.coverageSession = cluster, source
				ok, err := d.gatekeeperInstalled(context.Background())
				want := operations.SourceAPIFailed
				if supportedTemplateIdentity(mode) {
					want = operations.SourceAPIAdvertised
				}
				if ok != supportedTemplateIdentity(mode) || (err != nil) == supportedTemplateIdentity(mode) || calls != 1 {
					t.Fatal("ambiguous or inconsistent template metadata passed the gate", ok, err, calls)
				}
				snapshot, err := tracker.SnapshotCoverage(context.Background(), cluster, nil)
				if err != nil || len(snapshot.Sources) != 1 || snapshot.Sources[0].Discovery.State != want || snapshot.Sources[0].Discovery.Revision != 1 || snapshot.Sources[0].State == operations.CoverageObserved {
					t.Fatal("template check lost qualification or claimed collection success", snapshot, err)
				}
				encoded, err := json.Marshal(snapshot)
				if err != nil || strings.Contains(string(encoded), "foreign-canary") {
					t.Fatal("unsupported metadata leaked into source evidence", string(encoded), err)
				}
				other, err := tracker.SnapshotCoverage(context.Background(), "west", nil)
				if err != nil || len(other.Sources) != 0 {
					t.Fatal("template discovery evidence escaped member scope", other, err)
				}
				active = "explicit"
				if ok, err := d.gatekeeperInstalled(context.Background()); !ok || err != nil || calls != 2 {
					t.Fatal("fresh supported metadata could not recover the gate", ok, err, calls)
				}
				after, err := tracker.SnapshotCoverage(context.Background(), cluster, nil)
				if err != nil || after.Sources[0].Discovery.State != operations.SourceAPIAdvertised || after.Sources[0].Discovery.Revision != 2 || !coverageRow(after.Sources[0], coverageConstraintGVR).PersistenceGapSince.Equal(prior) {
					t.Fatal("API recovery erased prior write-loss evidence", after, err)
				}
			})
		}
	}
}

func TestTemplateAmbiguityPreventsInformerUntilFreshMetadata(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("cluster="+cluster, func(t *testing.T) {
			client, _ := coverageClient()
			tracker := operations.NewCoverageTracker(time.Minute)
			var valid atomic.Bool
			disco := templateDiscovery{read: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
				mode := "duplicate"
				if valid.Load() {
					mode = "explicit"
				}
				return &metav1.APIResourceList{GroupVersion: version, APIResources: templateIdentityRows(mode)}, nil
			}}
			store := memory.New()
			retained := graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "ConfigMap", Name: "retained"}
			if err := store.UpsertResource(context.Background(), retained); err != nil {
				t.Fatal(err)
			}
			d := New(client, store, nilRegistry{}, discovery.NewDynamicInformerManager(client), WithClusterID(cluster), WithCoverage(tracker), WithDiscovery(disco))
			d.pollEvery = 10 * time.Millisecond
			startCoverageDiscovery(t, d)
			waitFor(t, 3*time.Second, "ambiguous API evidence", func() bool {
				source := memberGKSource(t, tracker, cluster)
				return source.Discovery != nil && source.Discovery.State == operations.SourceAPIFailed
			})
			if len(client.Actions()) != 0 || len(d.dynMgr.ActiveGVRs()) != 0 {
				t.Fatal("ambiguous API metadata started list/watch", client.Actions())
			}
			if _, err := store.GetResource(context.Background(), retained.ID()); err != nil {
				t.Fatal("failed template discovery removed retained data", err)
			}
			valid.Store(true)
			waitFor(t, 3*time.Second, "supported API starts existing collectors", func() bool {
				_, object := coverageObjects()
				r := discovery.UnstructuredToResource(object, "K8sRequiredLabels")
				r.ClusterID = cluster
				_, err := store.GetResource(context.Background(), r.ID())
				return err == nil && memberGKSource(t, tracker, cluster).Discovery.State == operations.SourceAPIAdvertised
			})
			for _, action := range client.Actions() {
				if action.GetResource().Resource == "secrets" {
					t.Fatal("template recovery read Secret objects")
				}
			}
		})
	}
}

func TestTemplateIdentityProductionClientUsesOneMetadataRead(t *testing.T) {
	for _, mode := range []string{"implicit", "explicit", "wrong-row-group", "wrong-row-version", "duplicate", "late-duplicate"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/apis/templates.gatekeeper.sh/v1" {
					t.Error("template gate requested another discovery/object path", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(&metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gkGV(), APIResources: templateIdentityRows(mode)}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client, err := k8sdiscovery.NewDiscoveryClientForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			d := newAwaitDiscovery(client, time.Hour)
			state, err := d.discoverTemplateAPI(context.Background())
			if (state == operations.SourceAPIAdvertised) != supportedTemplateIdentity(mode) || (err == nil) != supportedTemplateIdentity(mode) || calls.Load() != 1 {
				t.Fatal("production client accepted inconsistent metadata or repeated requests", state, err, calls.Load())
			}
		})
	}
}
