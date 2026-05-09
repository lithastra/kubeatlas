// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/openapi"
	"k8s.io/client-go/rest"
	openapi_v2 "github.com/google/gnostic-models/openapiv2"
)

// fakeDiscoveryWithGroups builds a DiscoveryInterface that returns
// exactly the groups the test names. Avoids spinning up a fake
// Kubernetes apiserver for what is effectively a slice scan.
type fakeDiscoveryWithGroups struct {
	discovery.DiscoveryInterface
	groups []metav1.APIGroup
	err    error
}

func (f *fakeDiscoveryWithGroups) ServerGroups() (*metav1.APIGroupList, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &metav1.APIGroupList{Groups: f.groups}, nil
}

// Compile-time assurance: only override ServerGroups; everything
// else falls through to the embedded DiscoveryInterface (nil here,
// which is fine because the test never calls those methods).
var _ discovery.DiscoveryInterface = (*fakeDiscoveryWithGroups)(nil)

// Suppress unused-import warnings; these are kept available for
// future tests that exercise the broader DiscoveryInterface surface.
var (
	_ = fakediscovery.FakeDiscovery{}
	_ = fake.NewSimpleClientset
	_ = openapi.Client(nil)
	_ = rest.Config{}
	_ = runtime.Scheme{}
	_ = version.Info{}
	_ = openapi_v2.Document{}
)

// TestDetectOpenShift_PositiveAndNegative covers the happy and not-
// detected branches with a typed fake.
func TestDetectOpenShift_PositiveAndNegative(t *testing.T) {
	cases := []struct {
		name   string
		groups []metav1.APIGroup
		want   bool
	}{
		{"positive: route.openshift.io present", []metav1.APIGroup{
			{Name: "route.openshift.io"},
			{Name: "apps.openshift.io"},
		}, true},
		{"negative: only core + apps", []metav1.APIGroup{
			{Name: ""},
			{Name: "apps"},
		}, false},
		{"negative: empty groups", []metav1.APIGroup{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DetectOpenShift(&fakeDiscoveryWithGroups{groups: c.groups})
			if err != nil {
				t.Fatalf("DetectOpenShift: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestDetectOpenShift_NilClient: defensive nil check rather than a
// nil-deref on the first method call.
func TestDetectOpenShift_NilClient(t *testing.T) {
	_, err := DetectOpenShift(nil)
	if err == nil {
		t.Fatal("expected error for nil client, got nil")
	}
}

// TestDetectOpenShift_DiscoveryError: surface ServerGroups errors
// wrapped (errors.Is reaches the underlying cause). Caller decides
// whether to warn-and-degrade or fail.
func TestDetectOpenShift_DiscoveryError(t *testing.T) {
	sentinel := errors.New("network down")
	_, err := DetectOpenShift(&fakeDiscoveryWithGroups{err: sentinel})
	if err == nil {
		t.Fatal("expected wrapped error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err %v should wrap the sentinel", err)
	}
}

// TestParseRulePackMode normalizes operator inputs.
func TestParseRulePackMode(t *testing.T) {
	cases := map[string]RulePackMode{
		"":     RulePackModeAuto,
		"auto": RulePackModeAuto,
		"true": RulePackModeOn,
		"false": RulePackModeOff,
	}
	for in, want := range cases {
		got, err := ParseRulePackMode(in)
		if err != nil {
			t.Errorf("ParseRulePackMode(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseRulePackMode(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseRulePackMode("nope"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

// TestResolveOpenShiftLoad covers the four interesting branches:
// auto+detected, auto+not-detected, force-on, force-off — plus the
// detector-error degrade path.
func TestResolveOpenShiftLoad(t *testing.T) {
	openShiftCluster := &fakeDiscoveryWithGroups{groups: []metav1.APIGroup{
		{Name: "route.openshift.io"},
	}}
	plainCluster := &fakeDiscoveryWithGroups{groups: []metav1.APIGroup{
		{Name: "apps"},
	}}

	cases := []struct {
		name string
		mode RulePackMode
		disc discovery.DiscoveryInterface
		want bool
	}{
		{"auto + OpenShift", RulePackModeAuto, openShiftCluster, true},
		{"auto + plain k8s", RulePackModeAuto, plainCluster, false},
		{"force on, plain", RulePackModeOn, plainCluster, true},
		{"force off, OpenShift", RulePackModeOff, openShiftCluster, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveOpenShiftLoad(c.mode, c.disc)
			if err != nil {
				t.Fatalf("ResolveOpenShiftLoad: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}

	// Detector failure under auto returns false + an error so the
	// caller can log warn but still continue boot.
	got, err := ResolveOpenShiftLoad(RulePackModeAuto,
		&fakeDiscoveryWithGroups{err: errors.New("conn refused")})
	if got {
		t.Error("auto + detector error: expected load=false")
	}
	if err == nil || !strings.Contains(err.Error(), "conn refused") {
		t.Errorf("err = %v, want wrapped detector error", err)
	}
}
