package graph

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestResourceID_Namespaced(t *testing.T) {
	r := Resource{Kind: "Deployment", Name: "web-app", Namespace: "demo"}
	got := r.ID()
	want := "demo/Deployment/web-app"
	if got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
}

func TestResourceID_ClusterScoped(t *testing.T) {
	r := Resource{Kind: "Namespace", Name: "demo"}
	got := r.ID()
	want := "/Namespace/demo"
	if got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
}

func TestResource_OwnerReferencesMarshal(t *testing.T) {
	r := Resource{
		Kind:      "Pod",
		Name:      "web-abc",
		Namespace: "demo",
		OwnerReferences: []OwnerRef{
			{Kind: "ReplicaSet", Name: "web-1", UID: types.UID("rs-uid-1")},
			{Kind: "ReplicaSet", Name: "web-2", UID: types.UID("rs-uid-2")},
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round Resource
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(round.OwnerReferences) != 2 {
		t.Fatalf("OwnerReferences length = %d, want 2", len(round.OwnerReferences))
	}
	if round.OwnerReferences[0].UID != "rs-uid-1" {
		t.Errorf("first owner UID = %q, want %q", round.OwnerReferences[0].UID, "rs-uid-1")
	}
}

func TestResource_OmitEmptyKeepsPoCShape(t *testing.T) {
	// A Resource constructed with only the four PoC fields must marshal
	// without any of the W2-added keys, so PoC-era JSON parsers still
	// see a familiar shape.
	r := Resource{Kind: "Service", Name: "api", Namespace: "demo"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, key := range []string{"groupVersion", "uid", "annotations", "ownerReferences", "resourceVersion"} {
		if contains(got, key) {
			t.Errorf("expected %q to be omitted, got %s", key, got)
		}
	}
}

func TestEdgeType_ConstantValues(t *testing.T) {
	cases := map[EdgeType]string{
		EdgeTypeOwns:               "OWNS",
		EdgeTypeUsesConfigMap:      "USES_CONFIGMAP",
		EdgeTypeUsesSecret:         "USES_SECRET",
		EdgeTypeMountsVolume:       "MOUNTS_VOLUME",
		EdgeTypeSelects:            "SELECTS",
		EdgeTypeUsesServiceAccount: "USES_SERVICEACCOUNT",
		EdgeTypeRoutesTo:           "ROUTES_TO",
		EdgeTypeAttachedTo:         "ATTACHED_TO",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("EdgeType %q has value %q, want %q", want, string(got), want)
		}
	}
}

func TestAllEdgeTypes_CoversAllConstants(t *testing.T) {
	if len(AllEdgeTypes) != 8 {
		t.Fatalf("AllEdgeTypes length = %d, want 8", len(AllEdgeTypes))
	}
	seen := make(map[EdgeType]bool, len(AllEdgeTypes))
	for _, t := range AllEdgeTypes {
		seen[t] = true
	}
	for _, want := range []EdgeType{
		EdgeTypeOwns, EdgeTypeUsesConfigMap, EdgeTypeUsesSecret,
		EdgeTypeMountsVolume, EdgeTypeSelects, EdgeTypeUsesServiceAccount,
		EdgeTypeRoutesTo, EdgeTypeAttachedTo,
	} {
		if !seen[want] {
			t.Errorf("AllEdgeTypes missing %q", want)
		}
	}
}

func TestEdge_DeprecatedRelationStillRoundTrips(t *testing.T) {
	// PoC-era output that only set Relation (no Type) must still parse.
	pocJSON := `{"from":"a","to":"b","relation":"configMapRef"}`
	var e Edge
	if err := json.Unmarshal([]byte(pocJSON), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Relation != "configMapRef" {
		t.Errorf("Relation = %q, want configMapRef", e.Relation)
	}
	if e.Type != "" {
		t.Errorf("Type = %q, want empty (PoC payload had no type)", e.Type)
	}
}

func TestEdge_TypeAndRelationCoexist(t *testing.T) {
	// During the deprecation window, an Edge may have both Type (new)
	// and Relation (legacy mirror). JSON output should include both.
	e := Edge{From: "a", To: "b", Type: EdgeTypeUsesConfigMap, Relation: "configMapRef"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if !contains(got, `"type":"USES_CONFIGMAP"`) {
		t.Errorf("missing type field in %s", got)
	}
	if !contains(got, `"relation":"configMapRef"`) {
		t.Errorf("missing relation field in %s", got)
	}
}

func TestErrNotFound_FormatsID(t *testing.T) {
	err := ErrNotFound{ID: "demo/Pod/missing"}
	want := "resource not found: demo/Pod/missing"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// contains is a tiny helper so tests don't need to import strings.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
