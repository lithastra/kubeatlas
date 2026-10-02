// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package impact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// Query selects one server-side analysis. All bounds are explicit; no implicit
// cluster union, kind alias resolution, pagination, retries, or local fallback.
type Query struct {
	Namespace, Kind, Name, ClusterID, ExpectedUID string
	Relation                                      analysis.ImpactRelation
	MaxDepth, Limit                               int
}

func (q Query) Validate() error {
	if !validSegment(q.Kind, false) || !validSegment(q.Name, true) ||
		q.Namespace != "" && (!validSegment(q.Namespace, false) || q.Namespace == "_") ||
		q.ClusterID != "" && (!validSegment(q.ClusterID, false) || strings.Contains(q.ClusterID, ",")) ||
		q.ExpectedUID != "" && !validSegment(q.ExpectedUID, true) ||
		q.Relation != analysis.ImpactDependents && q.Relation != analysis.ImpactDependencies ||
		q.MaxDepth < 1 || q.MaxDepth > analysis.MaxImpactDepth || q.Limit < 1 || q.Limit > analysis.MaxImpactLimit {
		return errors.New("invalid impact query: use exact Kind/name, one cluster, dependents|dependencies, depth 1..10, limit 1..1000")
	}
	return nil
}

func validSegment(s string, colon bool) bool {
	if s == "" || len(s) > 253 || !utf8.ValidString(s) || s == "." || s == ".." || strings.ContainsAny(s, "/\\") || !colon && strings.Contains(s, ":") {
		return false
	}
	for _, ch := range s {
		if unicode.IsControl(ch) || unicode.IsSpace(ch) {
			return false
		}
	}
	return true
}

func (q Query) rootID() string {
	return (graph.Resource{Namespace: q.Namespace, Kind: q.Kind, Name: q.Name, ClusterID: q.ClusterID}).ID()
}

func (q Query) requestURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		(u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("invalid server URL: use an HTTP(S) base URL without credentials, query, or fragment")
	}
	// Plain HTTP is for a local tunnel only. No insecure-TLS switch and no
	// hostname-based exception: a literal loopback cannot resolve elsewhere.
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return "", errors.New("remote impact servers require HTTPS; for a local tunnel use a literal loopback address")
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", errors.New("invalid server URL path")
		}
	}
	ns := q.Namespace
	if ns == "" {
		ns = "_"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/impact/" + ns + "/" + q.Kind + "/" + q.Name
	u.RawPath = ""
	v := url.Values{"relation": {string(q.Relation)}, "max_depth": {strconv.Itoa(q.MaxDepth)}, "limit": {strconv.Itoa(q.Limit)}}
	if q.ClusterID != "" {
		v.Set("cluster", q.ClusterID)
	}
	if q.ExpectedUID != "" {
		v.Set("expected_uid", q.ExpectedUID)
	}
	u.RawQuery = v.Encode()
	return u.String(), nil
}

// Fetch makes exactly one bounded GET. Token is never encoded in a URL, errors,
// or a report. Redirects (even same-origin) are not followed. Use context to
// bound server discovery and this read together; the read also has a 30s cap.
func Fetch(ctx context.Context, base, token string, q Query) (*Response, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	dst, err := q.requestURL(base)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid bearer token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dst, nil)
	if err != nil {
		return nil, errors.New("cannot construct impact request")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		// net/http errors include URLs and sometimes credentials. Do not relay.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("impact request stopped: %w", ctx.Err())
		}
		return nil, errors.New("impact request failed: check server reachability, TLS trust, and timeout")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("impact server returned an unexpected content type")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, errors.New("impact response could not be read completely")
	}
	if len(body) > MaxResponseBytes {
		return nil, errors.New("impact response exceeds the 2 MiB limit")
	}
	var result Response
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Fail closed on unknown payload fields rather than exporting arbitrary
	// data from a wrong endpoint/new schema. The envelope is shared with v1.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, errors.New("impact response does not match the supported schema")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("impact response contains trailing data")
	}
	if err := validateResponse(&result, q); err != nil {
		return nil, err
	}
	return &result, nil
}

func statusError(status int) error {
	message := map[int]string{
		400: "invalid query or cluster selection", 401: "bearer authentication required (KUBEATLAS_TOKEN)",
		403: "not authorized for the selected cluster", 404: "impact API, cluster, or resource not found",
		409: "resource UID changed or is unknown; refresh the selected instance", 413: "analysis exceeds the server response budget",
		429: "analysis capacity busy; retry explicitly later", 503: "analysis evidence unavailable", 504: "analysis deadline exceeded",
	}[status]
	if message == "" {
		message = "unexpected server response (redirects are not followed)"
	}
	return fmt.Errorf("impact HTTP %d: %s", status, message)
}

func validateResponse(r *Response, q Query) error {
	bad := errors.New("impact response identity, scope, or query does not match the request")
	mode := "single"
	if q.ClusterID != "" {
		mode = "federated"
	}
	a := r.Analysis
	if r.SchemaVersion != "1" || r.KubeAtlasVersion == "" || r.GeneratedAt.IsZero() || r.SharingWarning != SharingWarning ||
		r.Scope.ClusterID != q.ClusterID || r.Scope.Mode != mode || r.Scope.NamespaceScope != "all_namespaces_in_selected_cluster" ||
		a == nil || a.Root.ID != q.rootID() || a.Root.Namespace != q.Namespace || a.Root.Kind != q.Kind || a.Root.Name != q.Name ||
		a.Root.ClusterID != q.ClusterID || q.ExpectedUID != "" && a.Root.UID != q.ExpectedUID ||
		a.Relation != q.Relation || a.MaxDepth != q.MaxDepth || a.Limit != q.Limit || a.Observation == nil || a.Availability == nil {
		return bad
	}
	if a.Authorization.MaxDepth != q.MaxDepth || a.Authorization.Limit != q.Limit ||
		a.Availability.Evidence.ClusterID != q.ClusterID ||
		a.Observation.Ordinary.Before.ClusterID != q.ClusterID || a.Observation.Ordinary.After.ClusterID != q.ClusterID ||
		a.Observation.Authorization.Before.ClusterID != q.ClusterID || a.Observation.Authorization.After.ClusterID != q.ClusterID {
		return bad
	}
	identities := map[string]bool{a.Root.ID: true}
	validIdentity := func(id analysis.ImpactIdentity) bool {
		expected := (graph.Resource{Namespace: id.Namespace, Kind: id.Kind, Name: id.Name, ClusterID: id.ClusterID}).ID()
		return id.ID == expected && id.ClusterID == q.ClusterID && id.Kind != "" && id.Name != "" &&
			(id.Kind != "Secret" || id.ReferenceOnly && id.UID == "" && id.ResourceVersion == "")
	}
	if !validIdentity(a.Root) || len(a.Resources) > q.Limit || len(a.Authorization.Resources) > q.Limit || a.Counts.Total != len(a.Resources) || a.Authorization.Counts.Total != len(a.Authorization.Resources) {
		return bad
	}
	direct := 0
	for _, match := range a.Resources {
		if match.Depth == 1 {
			direct++
		}
		if !validIdentity(match.Resource) || identities[match.Resource.ID] {
			return bad
		}
		identities[match.Resource.ID] = true
	}
	authIDs := map[string]bool{a.Root.ID: true}
	authDirect := 0
	for _, match := range a.Authorization.Resources {
		if match.Depth == 1 {
			authDirect++
		}
		if !validIdentity(match.Resource) || authIDs[match.Resource.ID] {
			return bad
		}
		authIDs[match.Resource.ID] = true
	}
	// Counts are derived from the captured paths, not trusted independently.
	// Apply the same direct/indirect contract as the Web capture validator.
	if a.Counts.Direct != direct || a.Counts.Indirect != a.Counts.Total-direct ||
		a.Authorization.Counts.Direct != authDirect || a.Authorization.Counts.Indirect != a.Authorization.Counts.Total-authDirect {
		return bad
	}
	validPath := func(path []analysis.ImpactStep, depth int, target string, ids map[string]bool) bool {
		if depth < 1 || depth > q.MaxDepth || len(path) != depth {
			return false
		}
		last := a.Root.ID
		for _, step := range path {
			forward := step.TraversalFrom == step.From && step.TraversalTo == step.To
			reverse := step.TraversalFrom == step.To && step.TraversalTo == step.From
			if !ids[step.From] || !ids[step.To] || step.TraversalFrom != last ||
				(!forward && !reverse) {
				return false
			}
			last = step.TraversalTo
		}
		return last == target
	}
	for _, match := range a.Resources {
		if !validPath(match.Path, match.Depth, match.Resource.ID, identities) {
			return bad
		}
	}
	for _, match := range a.Authorization.Resources {
		steps := make([]analysis.ImpactStep, len(match.Path))
		for i, step := range match.Path {
			steps[i] = step.ImpactStep
		}
		if !validPath(steps, match.Depth, match.Resource.ID, authIDs) {
			return bad
		}
	}
	for _, ref := range a.ReferenceEvidence {
		if !identities[ref.From] || !identities[ref.To] {
			return bad
		}
	}
	if retention := a.Availability.Evidence.History.RetentionEvidence; retention != nil && retention.Bounds.ClusterID != q.ClusterID {
		return bad
	}
	return nil
}
