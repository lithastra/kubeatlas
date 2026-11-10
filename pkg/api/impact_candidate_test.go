// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

const (
	candidatePlanBytes = 128 << 10
	candidateSamples   = 100
	candidateWarmups   = 10
	candidateTimeout   = 6 * time.Second
)

var (
	candidatePlanSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
	candidateCommit  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	candidateVersion = regexp.MustCompile(`^v?1\.7\.0(?:-[0-9A-Za-z.-]+)?$`)
	candidateLabel   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

type impactCandidatePlan struct {
	Commit      string                      `json:"commit"`
	Version     string                      `json:"version"`
	Tier        string                      `json:"tier"`
	Cases       []impactCandidateCase       `json:"cases"`
	Performance *impactCandidatePerformance `json:"performance,omitempty"`
}

type impactCandidateCase struct {
	Name              string                              `json:"name"`
	Query             impact.Query                        `json:"query"`
	Status            int                                 `json:"status"`
	Anonymous         bool                                `json:"anonymous,omitempty"`
	Paths             map[string][]analysis.ImpactStep    `json:"paths"`
	Truncated         bool                                `json:"truncated,omitempty"`
	Coverage          *impactCandidateCoverage            `json:"coverage,omitempty"`
	History           *impactCandidateHistory             `json:"history,omitempty"`
	Authorization     *impactCandidateAuthorization       `json:"authorization,omitempty"`
	ReferenceEvidence *[]analysis.ImpactReferenceEvidence `json:"referenceEvidence,omitempty"`
}

type impactCandidateCoverage struct {
	Source                   string
	Group, Version, Resource string
	State                    operations.CoverageState
	Reason                   operations.CoverageReason
}

type impactCandidateHistory struct {
	State          operations.HistoryAvailabilityState
	RetainedData   string
	Reason         operations.HistoryAvailabilityReason
	FirstEventAt   *time.Time
	LastEventAt    *time.Time
	LatestMarkerAt *time.Time
}

type impactCandidatePerformance struct {
	Case      string `json:"case"`
	Shape     string `json:"shape"`
	Resources int    `json:"resources"`
	Edges     int    `json:"edges"`
	Host      string `json:"host"`
}

func readImpactCandidatePlan(reader io.Reader, expectedSHA string) (impactCandidatePlan, error) {
	var plan impactCandidatePlan
	if !candidatePlanSHA.MatchString(expectedSHA) {
		return plan, errors.New("candidate plan requires a lowercase SHA-256 digest")
	}
	data, err := io.ReadAll(io.LimitReader(reader, candidatePlanBytes+1))
	if err != nil || len(data) > candidatePlanBytes {
		return plan, errors.New("candidate plan could not be read within its byte limit")
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != expectedSHA {
		return plan, errors.New("candidate plan SHA-256 mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF {
		return plan, errors.New("invalid candidate plan JSON")
	}
	return plan, plan.validate()
}

func (p impactCandidatePlan) validate() error {
	if !candidateCommit.MatchString(p.Commit) || !candidateVersion.MatchString(p.Version) ||
		(p.Tier != "tier1" && p.Tier != "tier2") || len(p.Cases) == 0 || len(p.Cases) > 24 {
		return errors.New("candidate plan requires a full commit, v1.7.0 version, tier and 1–24 cases")
	}
	seen := make(map[string]int, len(p.Cases))
	for i, c := range p.Cases {
		if !candidateLabel.MatchString(c.Name) || seen[c.Name] != 0 || c.Query.Validate() != nil {
			return fmt.Errorf("invalid or duplicate candidate case %d", i+1)
		}
		seen[c.Name] = c.Status
		switch c.Status {
		case http.StatusOK:
			if c.Paths == nil || len(c.Paths) > c.Query.Limit {
				return fmt.Errorf("case %d requires an explicit path oracle", i+1)
			}
			if !c.validPaths() {
				return fmt.Errorf("invalid path oracle in case %d", i+1)
			}
			if c.Coverage != nil && (!c.Coverage.valid()) {
				return fmt.Errorf("invalid coverage oracle in case %d", i+1)
			}
			if h := c.History; h != nil && !h.valid() {
				return fmt.Errorf("invalid history oracle in case %d", i+1)
			}
			if auth := c.Authorization; auth != nil && !auth.valid(c.Query) {
				return fmt.Errorf("invalid authorization oracle in case %d", i+1)
			}
			if c.ReferenceEvidence != nil && !validCandidateReferenceEvidence(c.Paths, *c.ReferenceEvidence) {
				return fmt.Errorf("invalid reference evidence oracle in case %d", i+1)
			}
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict:
			if c.Paths != nil || c.Coverage != nil || c.History != nil || c.Authorization != nil || c.ReferenceEvidence != nil || c.Truncated ||
				c.Status == http.StatusUnauthorized && !c.Anonymous ||
				c.Status == http.StatusForbidden && (c.Anonymous || c.Query.ClusterID == "") ||
				c.Status == http.StatusConflict && c.Query.ExpectedUID == "" {
				return fmt.Errorf("invalid rejection oracle in case %d", i+1)
			}
		default:
			return fmt.Errorf("unsupported expected status in case %d", i+1)
		}
	}
	if m := p.Performance; m != nil {
		if seen[m.Case] != http.StatusOK || strings.TrimSpace(m.Host) == "" || len(m.Host) > 256 ||
			m.Resources <= 0 || m.Edges <= 0 {
			return errors.New("performance requires a successful case and a declared host and graph shape")
		}
		switch m.Shape {
		case "baseline-5k", "dense-5k":
			if m.Resources != 5000 {
				return errors.New("5K profile requires 5,000 resources")
			}
		case "10k":
			if m.Resources != 10000 {
				return errors.New("10K profile requires 10,000 resources")
			}
		default:
			return errors.New("unknown performance shape")
		}
	}
	return nil
}

// validPaths checks the declared paths before a candidate request is made.
// A step can only start at the root or a declared match and must follow the
// query direction. Ownership expectations are explicit for OWNS edges.
func (c impactCandidateCase) validPaths() bool {
	q := c.Query
	root := (graph.Resource{ClusterID: q.ClusterID, Namespace: q.Namespace, Kind: q.Kind, Name: q.Name}).ID()
	for target, steps := range c.Paths {
		if target == root || !graph.ImpactIDInCluster(target, q.ClusterID) || len(steps) == 0 || len(steps) > q.MaxDepth {
			return false
		}
		last := root
		visited := map[string]bool{root: true}
		for _, step := range steps {
			from, to := step.From, step.To
			if q.Relation == analysis.ImpactDependents {
				from, to = to, from
			}
			if from != last || step.TraversalFrom != from || step.TraversalTo != to ||
				visited[to] || !slices.Contains(graph.AllEdgeTypes, step.Type) {
				return false
			}
			if _, ok := c.Paths[to]; !ok {
				return false
			}
			if step.Type == graph.EdgeTypeOwns {
				if step.OwnerUIDStatus != "matched" && step.OwnerUIDStatus != "unknown" {
					return false
				}
			} else if step.OwnerUIDStatus != "" {
				return false
			}
			last, visited[to] = to, true
		}
		if last != target {
			return false
		}
	}
	return true
}

func candidateInfoURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" ||
		u.ForceQuery || strings.Contains(base, "#") || (u.Path != "" && u.Path != "/") {
		return "", errors.New("candidate server must use a literal HTTP loopback tunnel")
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return "", errors.New("candidate server must use a literal HTTP loopback tunnel with an explicit port")
	}
	u.Path, u.RawPath = "/api/v1/info", ""
	return u.String(), nil
}

func verifyImpactCandidateBuild(ctx context.Context, base, token string, p impactCandidatePlan) error {
	dst, err := candidateInfoURL(base)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, candidateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dst, nil)
	if err != nil {
		return errors.New("cannot construct candidate info request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("candidate build info request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("candidate build info returned HTTP %d", resp.StatusCode)
	}
	const infoCap = 8 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, infoCap+1))
	var info InfoResponse
	if err != nil || len(body) > infoCap || json.Unmarshal(body, &info) != nil ||
		info.Commit != p.Commit || info.Version != p.Version || info.GraphStoreVersion != "v2" {
		return errors.New("candidate build metadata does not match the plan")
	}
	return nil
}

func checkImpactCandidateCase(ctx context.Context, base, token, version, tier string, c impactCandidateCase) error {
	if c.Anonymous {
		token = ""
	}
	ctx, cancel := context.WithTimeout(ctx, candidateTimeout)
	defer cancel()
	resp, err := impact.Fetch(ctx, base, token, c.Query)
	if c.Status != http.StatusOK {
		var status *impact.HTTPStatusError
		if !errors.As(err, &status) || status.StatusCode != c.Status {
			return fmt.Errorf("expected HTTP %d rejection", c.Status)
		}
		return nil
	}
	if err != nil {
		return err
	}
	a := resp.Analysis
	evidence := a.Availability.Evidence
	if !evidence.Storage.DurabilityKnown || evidence.Storage.Durable != (tier == "tier2") {
		return errors.New("response storage durability differs from the declared tier")
	}
	if c.History != nil && !c.History.matches(evidence.History) {
		return errors.New("response lacks the expected history or retained-window evidence")
	}
	if resp.KubeAtlasVersion != version || a.Truncated != c.Truncated || len(a.Resources) != len(c.Paths) {
		return errors.New("response version, truncation or match count differs from the oracle")
	}
	for _, match := range a.Resources {
		if steps, ok := c.Paths[match.Resource.ID]; !ok || !slices.Equal(steps, match.Path) {
			return errors.New("response target or exact path differs from the oracle")
		}
	}
	if c.ReferenceEvidence != nil && !matchesCandidateReferenceEvidence(*c.ReferenceEvidence, a.ReferenceEvidence) {
		return errors.New("response reference evidence differs from the oracle")
	}
	if c.Authorization != nil && !c.Authorization.matches(a.Authorization) {
		return errors.New("response authorization evidence differs from the oracle")
	}
	if c.Coverage != nil && !c.Coverage.matches(a.Observation.Ordinary.After) {
		return errors.New("response lacks the expected endpoint coverage evidence")
	}
	return nil
}

func (c impactCandidateCoverage) valid() bool {
	switch c.Source {
	case "core", string(operations.CoverageSourceCRD), string(operations.CoverageSourceGatekeeper):
	default:
		return false
	}
	switch c.State {
	case operations.CoverageUnknown, operations.CoverageInitializing, operations.CoverageObserved, operations.CoveragePartial, operations.CoverageStale:
		return c.Version != "" && c.Resource != ""
	default:
		return false
	}
}

func (c impactCandidateCoverage) matches(snapshot operations.CoverageSnapshot) bool {
	rows := snapshot.Resources
	if c.Source != "core" {
		index := slices.IndexFunc(snapshot.Sources, func(source operations.CoverageSourceSnapshot) bool {
			return string(source.Source) == c.Source
		})
		if index == -1 {
			return false
		}
		rows = snapshot.Sources[index].Resources
	}
	return slices.ContainsFunc(rows, func(row operations.ResourceCoverage) bool {
		return row.Group == c.Group && row.Version == c.Version && row.Resource == c.Resource &&
			row.State == c.State && (c.Reason == "" || slices.Contains(row.Reasons, c.Reason))
	})
}

func (c impactCandidateHistory) valid() bool {
	switch c.State {
	case operations.HistoryUnknown, operations.HistoryDisabled, operations.HistoryUnsupported, operations.HistoryInitializing,
		operations.HistoryRecording, operations.HistoryDegraded, operations.HistoryStopped:
	default:
		return false
	}
	switch c.RetainedData {
	case "observed":
		return true
	case "unavailable", "not_queried":
		return c.FirstEventAt == nil && c.LastEventAt == nil && c.LatestMarkerAt == nil
	default:
		return false
	}
}

func (c impactCandidateHistory) matches(history operations.HistoryAvailability) bool {
	if history.State != c.State || history.RetainedData != c.RetainedData ||
		c.Reason != "" && !slices.Contains(history.Reasons, c.Reason) {
		return false
	}
	if c.RetainedData != "observed" {
		return true
	}
	evidence := history.RetentionEvidence
	if evidence == nil || evidence.State != operations.RetentionObserved || evidence.Stale || evidence.Stopped {
		return false
	}
	b := evidence.Bounds
	return (c.FirstEventAt == nil || b.FirstEventAt.Equal(*c.FirstEventAt)) &&
		(c.LastEventAt == nil || b.LastEventAt.Equal(*c.LastEventAt)) &&
		(c.LatestMarkerAt == nil || b.LatestMarkerAt.Equal(*c.LatestMarkerAt))
}

func sampleImpactCandidate(ctx context.Context, check func(context.Context) error) ([]time.Duration, error) {
	samples := make([]time.Duration, 0, candidateSamples)
	for i := range candidateWarmups + candidateSamples {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := time.Now()
		if err := check(ctx); err != nil {
			return nil, fmt.Errorf("request %d failed: %w", i+1, err)
		}
		if i >= candidateWarmups {
			samples = append(samples, time.Since(start))
		}
	}
	return samples, nil
}

// impactP95 returns the nearest rank without reordering the capture.
func impactP95(samples []time.Duration) time.Duration {
	ordered := slices.Clone(samples)
	slices.Sort(ordered)
	return ordered[(95*len(ordered)+99)/100-1]
}

// Run against an already prepared candidate through an explicit local tunnel.
// Ordinary tests skip this entry point. Its plan is intentionally external.
func TestImpactFrozenCandidate(t *testing.T) {
	path := os.Getenv("KUBEATLAS_IMPACT_CANDIDATE_PLAN")
	if path == "" {
		t.Skip("set KUBEATLAS_IMPACT_CANDIDATE_PLAN to opt in")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("cannot open candidate plan")
	}
	defer file.Close()
	planSHA := os.Getenv("KUBEATLAS_IMPACT_CANDIDATE_PLAN_SHA256")
	plan, err := readImpactCandidatePlan(file, planSHA)
	if err != nil {
		t.Fatal(err)
	}
	base, token := os.Getenv("KUBEATLAS_IMPACT_CANDIDATE_SERVER"), os.Getenv("KUBEATLAS_TOKEN")
	if strings.ContainsAny(token, "\r\n") {
		t.Fatal("invalid bearer token")
	}
	for _, c := range plan.Cases {
		if c.Status == http.StatusForbidden && token == "" {
			t.Fatal("member denial cases require an explicit bearer token")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	t.Logf("candidate plan-sha256=%s commit=%s version=%s declared-tier=%s", planSHA, plan.Commit, plan.Version, plan.Tier)
	if err := verifyImpactCandidateBuild(ctx, base, token, plan); err != nil {
		t.Fatalf("opening build check: %v", err)
	}

	for _, c := range plan.Cases {
		if err := checkImpactCandidateCase(ctx, base, token, plan.Version, plan.Tier, c); err != nil {
			t.Fatalf("case %s: %v", c.Name, err)
		}
		t.Logf("case=%s status=%d", c.Name, c.Status)
	}
	if m := plan.Performance; m != nil {
		i := slices.IndexFunc(plan.Cases, func(c impactCandidateCase) bool { return c.Name == m.Case })
		c := plan.Cases[i]
		samples, err := sampleImpactCandidate(ctx, func(ctx context.Context) error {
			return checkImpactCandidateCase(ctx, base, token, plan.Version, plan.Tier, c)
		})
		if err != nil {
			t.Fatal(err)
		}
		p95 := impactP95(samples)
		ns := make([]int64, len(samples))
		for i, sample := range samples {
			ns[i] = sample.Nanoseconds()
		}
		t.Logf("samples-ns=%v", ns)
		t.Logf("shape=%s declared-resources=%d declared-edges=%d host=%q case=%s depth=%d limit=%d warmups=%d samples=%d p95=%s",
			m.Shape, m.Resources, m.Edges, m.Host, m.Case, c.Query.MaxDepth, c.Query.Limit, candidateWarmups, candidateSamples, p95)
		if m.Shape == "baseline-5k" && p95 > time.Second {
			t.Error("5K baseline exceeds the 1 second p95 target")
		}
	}
	if err := verifyImpactCandidateBuild(ctx, base, token, plan); err != nil {
		t.Fatalf("closing build check: %v", err)
	}
	if !t.Failed() {
		t.Logf("candidate probe complete plan-sha256=%s", planSHA)
	}
}
