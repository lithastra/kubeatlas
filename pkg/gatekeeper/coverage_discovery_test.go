// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sdiscovery "k8s.io/client-go/discovery"
)

type templateDiscovery struct {
	k8sdiscovery.DiscoveryInterface
	read func(context.Context, string) (*metav1.APIResourceList, error)
}

func (f templateDiscovery) ServerResourcesForGroupVersionWithContext(ctx context.Context, gv string) (*metav1.APIResourceList, error) {
	return f.read(ctx, gv)
}

func TestGatekeeperExactTemplateDiscoveryEvidence(t *testing.T) {
	for _, name := range []string{"advertised", "group_only", "subresource_only", "not_found", "denied", "unauthorized", "failed", "nil", "wrong_group", "wrong_kind", "namespaced", "no_list", "no_watch", "no_client"} {
		t.Run(name, func(t *testing.T) {
			want := operations.SourceAPIFailed
			resources := &metav1.APIResourceList{GroupVersion: gkGV(), APIResources: []metav1.APIResource{{Name: "constrainttemplates", Kind: "ConstraintTemplate", Verbs: metav1.Verbs{"list", "watch"}}}}
			var probeErr error
			switch name {
			case "advertised":
				want = operations.SourceAPIAdvertised
			case "group_only":
				resources.APIResources = nil
				want = operations.SourceAPINotAdvertised
			case "subresource_only":
				resources.APIResources[0].Name += "/status"
				want = operations.SourceAPINotAdvertised
			case "not_found":
				probeErr = apierrors.NewNotFound(constraintTemplateGVR.GroupResource(), "")
				want = operations.SourceAPINotAdvertised
			case "denied":
				probeErr = apierrors.NewForbidden(constraintTemplateGVR.GroupResource(), "", errors.New("discovery-error-canary"))
				want = operations.SourceAPIDenied
			case "unauthorized":
				probeErr = apierrors.NewUnauthorized("discovery-error-canary")
				want = operations.SourceAPIDenied
			case "failed":
				probeErr = errors.New("discovery-error-canary")
			case "nil":
				resources = nil
			case "wrong_group":
				resources.GroupVersion = "other.example.com/v1"
			case "wrong_kind":
				resources.APIResources[0].Kind = "Other"
			case "namespaced":
				resources.APIResources[0].Namespaced = true
			case "no_list":
				resources.APIResources[0].Verbs = metav1.Verbs{"watch"}
			case "no_watch":
				resources.APIResources[0].Verbs = metav1.Verbs{"list"}
			case "no_client":
				want = operations.SourceAPIUnknown
			}
			tracker := operations.NewCoverageTracker(time.Minute)
			source, err := tracker.BeginSource("", operations.CoverageSourceGatekeeper, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			d := newAwaitDiscovery(templateDiscovery{read: func(ctx context.Context, gv string) (*metav1.APIResourceList, error) {
				if gv != gkGV() {
					t.Error("wrong discovery target")
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Error("unbounded production discovery")
				}
				return resources, probeErr
			}}, time.Hour)
			d.coverageSession = source
			if name == "no_client" {
				d.disco = nil
			}
			ok, err := d.gatekeeperInstalled(context.Background())
			if ok != (name == "advertised" || name == "no_client") || (err != nil) != (want == operations.SourceAPIFailed || want == operations.SourceAPIDenied) {
				t.Fatalf("unexpected gate: %v %v", ok, err)
			}
			snapshot, err := tracker.SnapshotCoverage(context.Background(), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			evidence := snapshot.Sources[0].Discovery
			if evidence.State != want || evidence.Revision != 1 || evidence.CheckedAt.IsZero() != (name == "no_client") {
				t.Fatalf("wrong evidence: %+v", evidence)
			}
			encoded, _ := json.Marshal(snapshot)
			if strings.Contains(string(encoded), "discovery-error-canary") || snapshot.Sources[0].State == operations.CoverageObserved {
				t.Fatal("probe exposed error or proved collection completeness")
			}
		})
	}
}

func TestGatekeeperContextualDiscoveryCancellation(t *testing.T) {
	called := false
	d := newAwaitDiscovery(templateDiscovery{read: func(ctx context.Context, _ string) (*metav1.APIResourceList, error) {
		called = true
		<-ctx.Done()
		return nil, ctx.Err()
	}}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if state, err := d.discoverTemplateAPI(ctx); state != operations.SourceAPIUnknown || !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled request reached discovery")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if state, err := d.discoverTemplateAPI(ctx); state != operations.SourceAPIFailed || !errors.Is(err, context.DeadlineExceeded) || !called {
		t.Fatal("contextual request ignored cancellation")
	}
}
