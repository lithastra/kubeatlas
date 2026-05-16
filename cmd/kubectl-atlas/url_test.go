// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestResourceURL(t *testing.T) {
	got := resourceURL("http://atlas.example", "petclinic", "Deployment", "api")
	want := "http://atlas.example/resources/petclinic/Deployment/api"
	if got != want {
		t.Errorf("resourceURL = %q, want %q", got, want)
	}
}

func TestResourceURL_ClusterScopedUsesSentinel(t *testing.T) {
	// A cluster-scoped resource has no namespace; the UI route needs
	// the "_" sentinel because an empty path segment is unaddressable.
	got := resourceURL("http://atlas.example", "", "Node", "worker-1")
	want := "http://atlas.example/resources/_/Node/worker-1"
	if got != want {
		t.Errorf("resourceURL = %q, want %q", got, want)
	}
}

func TestResourceURL_EscapesPathSegments(t *testing.T) {
	got := resourceURL("http://atlas.example", "ns", "Pod", "weird/name")
	want := "http://atlas.example/resources/ns/Pod/weird%2Fname"
	if got != want {
		t.Errorf("resourceURL = %q, want %q", got, want)
	}
}

func TestNamespaceURL(t *testing.T) {
	got := namespaceURL("http://atlas.example", "petclinic")
	want := "http://atlas.example/topology?level=namespace&namespace=petclinic"
	if got != want {
		t.Errorf("namespaceURL = %q, want %q", got, want)
	}
}

func TestClusterURL(t *testing.T) {
	got := clusterURL("http://atlas.example")
	want := "http://atlas.example/topology?level=cluster"
	if got != want {
		t.Errorf("clusterURL = %q, want %q", got, want)
	}
}

func TestNormalizeBase_TrimsTrailingSlash(t *testing.T) {
	// A --server value with a trailing slash must not double the
	// separator when the path is appended.
	got := resourceURL("http://atlas.example/", "ns", "Pod", "p")
	want := "http://atlas.example/resources/ns/Pod/p"
	if got != want {
		t.Errorf("resourceURL with trailing-slash base = %q, want %q", got, want)
	}
}
