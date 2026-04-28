package graph

import "testing"

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
