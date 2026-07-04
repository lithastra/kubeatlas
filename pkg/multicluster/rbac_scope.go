// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package multicluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// F-206 multi-cluster RBAC visibility (P5-T7).
//
// This is READ-SIDE visibility only (invariant 2.4): it filters which
// clusters a caller may see, keyed on the bearer token they present. It
// deliberately does nothing about credential acquisition or rotation,
// nothing about OIDC/SSO, and adds no CRDs — the rules are plain Helm
// values. When no rules are configured it is a complete no-op and every
// caller sees every cluster, exactly as v1.4 behaved.

// RBACRule maps a bearer token to the clusters a caller presenting that
// token may see. It is the JSON shape the Helm chart renders from
// multicluster.rbac.rules (each rule's tokenSecretRef resolves to the
// token value, its clusters list to Clusters).
type RBACRule struct {
	Token    string   `json:"token"`
	Clusters []string `json:"clusters"`
}

// RBACScope resolves a request's bearer token to the set of clusters it
// is authorised to see. Tokens are stored only as SHA-256 hashes so a
// heap dump or a stray log line never leaks the plaintext.
type RBACScope struct {
	// byTokenHash maps sha256(token) -> the allowed cluster set. A
	// non-empty map means RBAC is active; an empty map is the disabled
	// (all-visible) default.
	byTokenHash map[string]map[string]struct{}
}

// NewRBACScope builds a scope from rules. Rules with an empty token are
// skipped. An empty (or all-skipped) rule set yields a disabled scope
// (Enabled()==false): the backward-compatible default where every
// caller sees every cluster.
func NewRBACScope(rules []RBACRule) *RBACScope {
	m := make(map[string]map[string]struct{}, len(rules))
	for _, r := range rules {
		tok := strings.TrimSpace(r.Token)
		if tok == "" {
			continue
		}
		set := make(map[string]struct{}, len(r.Clusters))
		for _, c := range r.Clusters {
			if c = strings.TrimSpace(c); c != "" {
				set[c] = struct{}{}
			}
		}
		m[hashToken(tok)] = set
	}
	return &RBACScope{byTokenHash: m}
}

// Enabled reports whether any RBAC rules are configured. When false the
// scope is a no-op and every caller sees every cluster.
func (s *RBACScope) Enabled() bool { return s != nil && len(s.byTokenHash) > 0 }

// VisibleClusters resolves the request's bearer token to its allowed
// cluster set and an HTTP status. The status is:
//
//	0                        authorised — the returned allow-set applies;
//	                         a nil set means "unrestricted" (RBAC off).
//	401 (Unauthorized)       RBAC is on but the request carried no bearer
//	                         token.
//	403 (Forbidden)          RBAC is on and the token matched no rule.
//
// A non-zero status is always paired with a nil set: the caller rejects
// the request rather than returning 200 with an empty body, so an
// unauthorised caller can tell "denied" from "no such cluster".
func (s *RBACScope) VisibleClusters(r *http.Request) (map[string]struct{}, int) {
	if !s.Enabled() {
		return nil, 0 // disabled: unrestricted, all clusters visible
	}
	tok := bearerToken(r)
	if tok == "" {
		return nil, http.StatusUnauthorized
	}
	set, found := s.byTokenHash[hashToken(tok)]
	if !found {
		return nil, http.StatusForbidden
	}
	return set, 0
}

// LoadRBACConfig reads the F-206 visibility rules from the environment:
//
//	KUBEATLAS_MULTICLUSTER_RBAC_RULES_FILE  path to a JSON rules file
//	                                        (the Helm chart mounts a Secret here)
//	KUBEATLAS_MULTICLUSTER_RBAC_RULES       the same JSON inline (tests / simple installs)
//
// Both hold a JSON array of {"token","clusters"} objects. Unset/empty
// yields no rules — the all-visible default. A malformed value is an
// error so a mis-mounted Secret fails the pod at startup rather than
// silently disabling access control (fail closed on config, open on
// absence). The file form wins if both are set.
func LoadRBACConfig() ([]RBACRule, error) {
	raw := strings.TrimSpace(os.Getenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES"))
	if path := strings.TrimSpace(os.Getenv("KUBEATLAS_MULTICLUSTER_RBAC_RULES_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("multicluster rbac: read rules file %q: %w", path, err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		return nil, nil
	}
	var rules []RBACRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("multicluster rbac: parse rules: %w", err)
	}
	return rules, nil
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, or "" when absent/malformed.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
