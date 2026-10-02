// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package impact

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

//go:embed report.html
var reportHTML string

var reportTemplate = template.Must(template.New("impact").Parse(reportHTML))

// Render serializes one captured response without fetching/recomputing anything.
// HTML embeds the same JSON object as escaped text, never executable JavaScript.
func Render(r *Response, format string) ([]byte, error) {
	if r == nil || r.Analysis == nil {
		return nil, errors.New("missing captured impact response")
	}
	a := r.Analysis
	if err := validateResponse(r, Query{Namespace: a.Root.Namespace, Kind: a.Root.Kind, Name: a.Root.Name,
		ClusterID: r.Scope.ClusterID, Relation: a.Relation, MaxDepth: a.MaxDepth, Limit: a.Limit}); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, errors.New("cannot encode captured impact response")
	}
	switch format {
	case "json":
		return append(data, '\n'), nil
	case "text":
		return []byte(textReport(r)), nil
	case "html":
		var out bytes.Buffer
		view := struct {
			Response *Response
			Summary  string
			JSON     string
		}{r, textReport(r), string(data)}
		if err := reportTemplate.Execute(&out, view); err != nil {
			return nil, errors.New("cannot render impact HTML")
		}
		return out.Bytes(), nil
	default:
		return nil, errors.New("impact output must be text, json, or html")
	}
}

func emptyAssessment(a *analysis.ImpactResult) string {
	if a.Counts.Total != 0 {
		return "Observed matches, not predicted outages."
	}
	o := a.Observation.Ordinary
	if a.ModeledTraversalComplete && !a.Truncated && o.TypeScopeClosed && o.StableWindow && o.EmptyResultAssessment == "none_found_in_analyzed_scope" {
		return "No " + string(a.Relation) + " found in the analyzed scope; not a safety guarantee."
	}
	return "No " + string(a.Relation) + " observed; analysis incomplete."
}

func textReport(r *Response) string {
	a := r.Analysis
	var b strings.Builder
	line := func(format string, args ...any) { _, _ = fmt.Fprintf(&b, format+"\n", args...) }
	quoted := strconv.QuoteToASCII
	line("KubeAtlas impact analysis (captured, not live)")
	line("WARNING: %s", SharingWarning)
	line("Schema: %s | Server version: %s | Captured: %s", quoted(r.SchemaVersion), quoted(r.KubeAtlasVersion), r.GeneratedAt.UTC().Format(time.RFC3339Nano))
	line("Root: %s | UID: %s | Resource version: %s | Reference only: %t", quoted(a.Root.ID), quoted(a.Root.UID), quoted(a.Root.ResourceVersion), a.Root.ReferenceOnly)
	line("Cluster: %s | Mode: %s | Scope: %s", quoted(r.Scope.ClusterID), quoted(r.Scope.Mode), quoted(r.Scope.NamespaceScope))
	line("Relation: %s | Max depth: %d | Per-facet limit: %d", a.Relation, a.MaxDepth, a.Limit)
	line("\nOrdinary relationships (authorization excluded)")
	line("Total: %d | Direct: %d | Indirect: %d | Workloads: %d | Pods: %d | Other: %d | Lower bound: %t",
		a.Counts.Total, a.Counts.Direct, a.Counts.Indirect, a.Counts.Workloads, a.Counts.Pods, a.Counts.Other, a.Counts.LowerBound)
	line("Modeled traversal complete: %t | Truncated: %t | Observation: %s", a.ModeledTraversalComplete, a.Truncated, quoted(a.ObservationCoverage))
	line("%s", emptyAssessment(a))
	line("Representative shortest paths; stored edges and traversal directions are distinct:")
	for _, match := range a.Resources {
		line("  depth=%d %s [uid=%s rv=%s referenceOnly=%t]", match.Depth, quoted(match.Resource.ID), quoted(match.Resource.UID), quoted(match.Resource.ResourceVersion), match.Resource.ReferenceOnly)
		for _, step := range match.Path {
			line("    walk %s -> %s; stored %s -[%s]-> %s; owner UID=%s", quoted(step.TraversalFrom), quoted(step.TraversalTo), quoted(step.From), quoted(string(step.Type)), quoted(step.To), quoted(step.OwnerUIDStatus))
		}
	}
	section := func(title string, value any) {
		// All values are typed response DTOs, with no arbitrary object payloads.
		encoded, _ := json.MarshalIndent(value, "", "  ")
		line("\n%s\n%s", title, encoded)
	}
	section("Traversal limits", a.TruncationReasons)
	section("Traversal notices", a.Notices)
	section("Reference declarations (not observed reloads or Secret values)", a.ReferenceEvidence)
	section("Authorization associations (separate counts; not effective permissions or credential-use proof)", a.Authorization)
	line("\nObservation coverage (including optional APIs and unobserved endpoints)")
	for _, facet := range []struct {
		name string
		data analysis.ImpactCoverageFacet
	}{{"ordinary", a.Observation.Ordinary}, {"authorization", a.Observation.Authorization}} {
		o := facet.data
		line("%s: state=%s profile=%s closedTypes=%t stableWindow=%t assessment=%s", facet.name,
			quoted(string(o.State)), quoted(o.Profile), o.TypeScopeClosed, o.StableWindow, quoted(o.EmptyResultAssessment))
		line("  Before: %s (%s, %d resource-type records) | After: %s (%s, %d resource-type records)",
			retainedTime(o.Before.CapturedAt), quoted(string(o.Before.State)), len(o.Before.Resources),
			retainedTime(o.After.CapturedAt), quoted(string(o.After.State)), len(o.After.Resources))
		section(facet.name+" coverage reasons", o.Reasons)
		section(facet.name+" optional APIs", o.OptionalAPIs)
		section(facet.name+" unobserved endpoints", o.UnobservedAPIs)
	}
	line("Detailed per-type/source observations and inventory remain in the complete JSON capture (--output json, or HTML's embedded JSON).")
	h := a.Availability.Evidence.History
	line("\nHistory writer: %s | Coverage: %s | Retained data: %s", quoted(string(h.State)), quoted(string(h.Coverage)), quoted(h.RetainedData))
	if e := h.RetentionEvidence; e != nil {
		line("Retention cache: %s | Stale: %t | Stopped: %t", quoted(string(e.State)), e.Stale, e.Stopped)
		line("Observed first event: %s | Last event: %s | Latest marker: %s", retainedTime(e.Bounds.FirstEventAt), retainedTime(e.Bounds.LastEventAt), retainedTime(e.Bounds.LatestMarkerAt))
	}
	line("Zero timestamps mean none observed/unknown. Event bounds do not prove continuous history; markers are not backups or verified recovery points.")
	section("Storage and history evidence (cached, metadata only)", a.Availability)
	// JSON protects ASCII control characters. Also neutralize Unicode format
	// controls (e.g. bidi overrides) in terminal-facing provider strings.
	var safe strings.Builder
	for _, ch := range b.String() {
		if (unicode.IsControl(ch) && ch != '\n' && ch != '\t') || unicode.Is(unicode.Cf, ch) {
			_, _ = fmt.Fprintf(&safe, "\\u%04x", ch)
		} else {
			safe.WriteRune(ch)
		}
	}
	return safe.String()
}

func retainedTime(t time.Time) string {
	if t.IsZero() {
		return "none observed / unknown"
	}
	return t.UTC().Format(time.RFC3339Nano)
}
