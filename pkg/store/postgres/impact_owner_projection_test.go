// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"strings"
	"testing"
)

func TestImpactOwnerProjectionBeforeWire(t *testing.T) {
	if testing.Short() {
		t.Skip("disposable PostgreSQL integration")
	}
	h := StartPostgresWithAGE(t)
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	r := graph.Resource{Kind: "Pod", Name: "child", Namespace: "default"}
	if err := s.UpsertResource(ctx, r); err != nil {
		t.Fatal(err)
	}
	for _, owners := range []string{
		`[ {"kind":"ReplicaSet","name":"parent","uid":"parent-uid","extra":{"payload":"owner-canary"}} ]`,
		`[ {"kind":{"payload":"owner-canary"},"name":"parent","uid":"parent-uid"} ]`,
		`{"payload":"owner-canary"}`,
	} {
		if _, err := s.pool.Exec(ctx, `UPDATE public.resources SET data=jsonb_set(data,'{ownerReferences}',$1::jsonb) WHERE id=$2`, owners, r.ID()); err != nil {
			t.Fatal(err)
		}
		var body []byte
		if err := s.pool.QueryRow(ctx, impactResourcesSQL, "", 2, graph.DefaultImpactBytes).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "canary") || strings.Contains(string(body), "extra") {
			t.Fatalf("unallowlisted owner data crossed wire: %s", body)
		}
		snapshot, err := s.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{})
		if strings.Contains(owners, `"extra"`) {
			if err != nil {
				t.Fatal(err)
			}
			if got := snapshot.Resources[0].OwnerReferences; len(got) != 1 || got[0].Name != "parent" || got[0].UID != "parent-uid" {
				t.Fatalf("owner identity changed: %+v", got)
			}
		} else if err == nil {
			t.Fatal("malformed owner identity accepted")
		}
	}
}
