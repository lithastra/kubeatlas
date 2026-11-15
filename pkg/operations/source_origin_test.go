// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

func TestOriginRetirementReplacementAndRestartNeverAdoptStoredData(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("scope="+cluster, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			source, err := tracker.BeginSource(cluster, operations.CoverageSourceCRD, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			r := storetest.ResourceOriginFixture(cluster)
			owner := r.Origin
			begin := func() *operations.CoverageSession {
				token, err := source.BeginType(owner.GVR(), time.Now())
				if err != nil || !token.BindOrigin(owner) {
					t.Fatal("cannot bind owner", err)
				}
				return token
			}
			snapshot := func() operations.CoverageSnapshot {
				got, err := tracker.SnapshotCoverage(context.Background(), cluster, nil)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			token := begin()
			r = token.StampOrigin(r)
			if !r.Origin.Matches(r) || graph.OriginQualification(r, snapshot().Origins) != "registered" {
				t.Fatal("missing committed writer binding")
			}
			foreign := r
			foreign.ClusterID = "west"
			if token.StampOrigin(foreign).Origin != (graph.ResourceOrigin{}) {
				t.Fatal("cross-member stamp")
			}
			before := snapshot()
			token.Stop(time.Now())
			if graph.OriginQualification(r, snapshot().Origins) != "retired" || token.StampOrigin(r).Origin != (graph.ResourceOrigin{}) {
				t.Fatal("stopped writer remained current")
			}
			fresh := begin()
			if graph.OriginQualification(r, snapshot().Origins) != "replaced" {
				t.Fatal("same-name owner adopted stored row")
			}
			updated := fresh.StampOrigin(r)
			if updated.Origin.Registration == r.Origin.Registration || graph.OriginQualification(updated, snapshot().Origins) != "registered" {
				t.Fatal("fresh receipt not established")
			}
			if graph.OriginQualification(r, before.Origins) != "registered" {
				t.Fatal("snapshot aliases changed after retirement")
			}
			freshTracker := operations.NewCoverageTracker(time.Minute)
			restarted, _ := freshTracker.SnapshotCoverage(context.Background(), cluster, nil)
			if graph.OriginQualification(updated, restarted.Origins) != "unknown" {
				t.Fatal("restart fabricated persistent lifecycle")
			}
			body, err := json.Marshal(snapshot())
			if err != nil || strings.Contains(string(body), owner.OwnerUID) || strings.Contains(string(body), updated.Origin.Registration) {
				t.Fatal("private owner escaped coverage JSON", string(body), err)
			}
		})
	}
}
