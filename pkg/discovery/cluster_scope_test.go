// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package discovery

import (
	"reflect"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestMemberEdgeScopeQualificationAndRejection(t *testing.T) {
	for _, tc := range []struct {
		from, to, wantFrom, wantTo string
		fail                       bool
	}{
		{"default/Foo/demo", "/Bar/target", "east:default/Foo/demo", "east:/Bar/target", false},
		{"east:default/Foo/demo", "_external/IAMRole/arn:aws:iam::123:role/demo", "east:default/Foo/demo", "east:_external/IAMRole/arn:aws:iam::123:role/demo", false},
		{"west:default/Foo/demo", "/Bar/target", "", "", true},
		{"default/Foo/demo", "west:/Bar/target", "", "", true},
		{"", "/Bar/target", "", "", true},
	} {
		input := []graph.Edge{{From: tc.from, To: tc.to, Type: graph.EdgeTypeUsesConfigMap}}
		original := append([]graph.Edge(nil), input...)
		out, err := ScopeEdgesToCluster(input, "east")
		if (err != nil) != tc.fail {
			t.Fatalf("wrong rejection for %+v: %v", tc, err)
		}
		if !tc.fail && (out[0].From != tc.wantFrom || out[0].To != tc.wantTo) {
			t.Fatalf("wrong member projection %+v", out)
		}
		if !reflect.DeepEqual(input, original) {
			t.Fatal("projection mutated caller's cached edges")
		}
		single, err := ScopeEdgesToCluster(input, "")
		if err != nil || !reflect.DeepEqual(single, input) {
			t.Fatal("standalone edge contract changed")
		}
	}
	edges := []graph.Edge{{From: "ns/Foo/demo", To: "/Bar/good"}, {From: "ns/Foo/demo", To: "west:/Bar/foreign"}}
	if out, err := ScopeEdgesToCluster(edges, "east"); err == nil || out != nil {
		t.Fatal("foreign endpoint returned a persistable partial set")
	}
}
