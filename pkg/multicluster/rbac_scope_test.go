// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package multicluster

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func req(token string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestRBACScope_DisabledIsUnrestricted(t *testing.T) {
	s := NewRBACScope(nil)
	if s.Enabled() {
		t.Fatal("empty scope should be disabled")
	}
	// No token required, and the allow-set is nil (unrestricted).
	allow, status := s.VisibleClusters(req(""))
	if status != 0 || allow != nil {
		t.Fatalf("disabled scope: got (%v, %d), want (nil, 0)", allow, status)
	}
}

func TestRBACScope_MatchedTokenAllowsItsClusters(t *testing.T) {
	s := NewRBACScope([]RBACRule{{Token: "tok-a", Clusters: []string{"cluster-a", "cluster-b"}}})
	if !s.Enabled() {
		t.Fatal("scope with a rule should be enabled")
	}
	allow, status := s.VisibleClusters(req("tok-a"))
	if status != 0 {
		t.Fatalf("status = %d, want 0 (authorised)", status)
	}
	for _, c := range []string{"cluster-a", "cluster-b"} {
		if _, ok := allow[c]; !ok {
			t.Errorf("%s should be visible", c)
		}
	}
	if _, ok := allow["cluster-c"]; ok {
		t.Error("cluster-c must NOT be visible to tok-a")
	}
}

func TestRBACScope_MissingTokenIs401(t *testing.T) {
	s := NewRBACScope([]RBACRule{{Token: "tok-a", Clusters: []string{"a"}}})
	if _, status := s.VisibleClusters(req("")); status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestRBACScope_UnknownTokenIs403(t *testing.T) {
	s := NewRBACScope([]RBACRule{{Token: "tok-a", Clusters: []string{"a"}}})
	if _, status := s.VisibleClusters(req("wrong-token")); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
}

func TestRBACScope_EmptyTokenRuleSkipped(t *testing.T) {
	s := NewRBACScope([]RBACRule{{Token: "  ", Clusters: []string{"a"}}})
	if s.Enabled() {
		t.Fatal("a rule with a blank token should be skipped, leaving the scope disabled")
	}
}

func TestRBACScope_TokensStoredHashed(t *testing.T) {
	s := NewRBACScope([]RBACRule{{Token: "super-secret", Clusters: []string{"a"}}})
	for k := range s.byTokenHash {
		if strings.Contains(k, "super-secret") {
			t.Fatal("stored key must be a hash, never the plaintext token")
		}
		if len(k) != 64 {
			t.Errorf("key %q is not a sha256 hex digest (len %d)", k, len(k))
		}
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct{ header, want string }{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},   // scheme is case-insensitive
		{"Bearer  abc ", "abc"}, // surrounding whitespace trimmed
		{"Basic abc", ""},       // wrong scheme
		{"", ""},
		{"abc", ""},
	}
	for _, tc := range cases {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		if got := bearerToken(r); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestLoadRBACConfig_Inline(t *testing.T) {
	t.Setenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES", `[{"token":"t1","clusters":["a"]}]`)
	rules, err := LoadRBACConfig()
	if err != nil {
		t.Fatalf("LoadRBACConfig: %v", err)
	}
	if len(rules) != 1 || rules[0].Token != "t1" || len(rules[0].Clusters) != 1 || rules[0].Clusters[0] != "a" {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestLoadRBACConfig_File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`[{"token":"t2","clusters":["b","c"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES_FILE", path)
	rules, err := LoadRBACConfig()
	if err != nil {
		t.Fatalf("LoadRBACConfig: %v", err)
	}
	if len(rules) != 1 || rules[0].Token != "t2" || len(rules[0].Clusters) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestLoadRBACConfig_UnsetIsNoRules(t *testing.T) {
	// Neither env var set (t.Setenv from other tests is already restored).
	t.Setenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES", "")
	t.Setenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES_FILE", "")
	rules, err := LoadRBACConfig()
	if err != nil {
		t.Fatalf("LoadRBACConfig: %v", err)
	}
	if rules != nil {
		t.Fatalf("want nil rules, got %+v", rules)
	}
}

func TestLoadRBACConfig_MalformedFailsClosed(t *testing.T) {
	t.Setenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES", `{not valid json`)
	if _, err := LoadRBACConfig(); err == nil {
		t.Fatal("malformed rules must error so config never silently disables access control")
	}
}

func TestLoadRBACConfig_MissingFileErrors(t *testing.T) {
	t.Setenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES_FILE", filepath.Join(t.TempDir(), "nope.json"))
	if _, err := LoadRBACConfig(); err == nil {
		t.Fatal("a missing rules file must error")
	}
}
