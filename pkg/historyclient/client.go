// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package historyclient reads one bounded, explicitly scoped history response.
// It never starts a cluster, discovers credentials or falls back to another API.
package historyclient

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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
)

type Query struct {
	Diff                           bool
	ClusterID, From, To, Namespace string
}
type ListResponse struct {
	Snapshots []graph.SnapshotMeta `json:"snapshots"`
	Count     int                  `json:"count"`
}

func validSegment(s string) bool {
	if s == "" || len(s) > 253 || !utf8.ValidString(s) || strings.ContainsAny(s, "/:\\,") {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (q Query) Validate() error {
	if q.ClusterID != "" && !validSegment(q.ClusterID) || q.Namespace != "" && !validSegment(q.Namespace) {
		return errors.New("select one exact cluster and optional namespace")
	}
	if !q.Diff {
		if q.From != "" || q.To != "" || q.Namespace != "" {
			return errors.New("time and namespace apply only to snapshot diff")
		}
		return nil
	}
	if q.From == "" {
		return errors.New("snapshot diff requires --from")
	}
	now := time.Now()
	from, err := readTime(q.From, now)
	if err != nil {
		return err
	}
	to, err := readTime(q.To, now)
	if err != nil {
		return err
	}
	if !from.Before(to) {
		return errors.New("history window must have from before to")
	}
	return nil
}
func readTime(raw string, now time.Time) (time.Time, error) {
	if raw == "" || raw == "now" {
		return now, nil
	}
	if d, err := snapshot.ParseRetention(raw); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("history time must be now, a positive duration or RFC3339 timestamp")
}
func (q Query) requestURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("use an HTTP(S) server base URL without credentials, query or fragment")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return "", errors.New("remote history servers require HTTPS; local HTTP requires a literal loopback address")
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", errors.New("invalid server URL path")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/snapshots"
	u.RawPath = ""
	v := url.Values{}
	if q.ClusterID != "" {
		v.Set("cluster", q.ClusterID)
	}
	if q.Diff {
		u.Path += "/diff"
		v.Set("from", q.From)
		if q.To != "" {
			v.Set("to", q.To)
		}
		if q.Namespace != "" {
			v.Set("namespace", q.Namespace)
		}
	}
	u.RawQuery = v.Encode()
	return u.String(), nil
}

// Fetch issues one GET, without redirects, retries, API fallback or payload
// export. It validates metadata before serialization. A diff has no cluster echo;
// its scope is bound to the requested URL and the trusted server's authorization.
func Fetch(ctx context.Context, base, token string, q Query) ([]byte, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	endpoint, err := q.requestURL(base)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid bearer token")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("cannot construct history request")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("history request stopped: %w", ctx.Err())
		}
		return nil, errors.New("history request failed: check reachability, TLS trust and timeout")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, statusError(resp.StatusCode)
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, errors.New("history server returned an unexpected content type")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, graph.MaxHistoryBytes+1))
	if err != nil {
		return nil, errors.New("history response could not be read completely")
	}
	if len(body) > graph.MaxHistoryBytes {
		return nil, errors.New("history response exceeds 2 MiB")
	}
	var value any
	if q.Diff {
		var result analysis.DiffResult
		if err := decode(body, &result); err != nil {
			return nil, err
		}
		if result.From.IsZero() || !result.From.Before(result.To) || result.Added == nil || result.Modified == nil || result.Removed == nil {
			return nil, errors.New("invalid history diff envelope")
		}
		for raw, actual := range map[string]time.Time{q.From: result.From, q.To: result.To} {
			if expected, err := time.Parse(time.RFC3339, raw); err == nil && !actual.Equal(expected) {
				return nil, errors.New("history response time window mismatch")
			}
		}
		if len(result.Added)+len(result.Modified)+len(result.Removed) > graph.MaxHistoryEvents {
			return nil, errors.New("history diff exceeds row limit")
		}
		for _, bucket := range [][]analysis.DiffEntry{result.Added, result.Modified, result.Removed} {
			for _, e := range bucket {
				if e.Kind == "Secret" || !validSegment(e.Kind) || !validSegment(e.Name) || e.Namespace != "" && !validSegment(e.Namespace) || q.Namespace != "" && e.Namespace != q.Namespace || e.Timestamp.Before(result.From) || e.Timestamp.After(result.To) || (e.EventType != graph.EventTypeAdd && e.EventType != graph.EventTypeUpdate && e.EventType != graph.EventTypeDelete) {
					return nil, errors.New("invalid history diff metadata")
				}
			}
		}
		value = result
	} else {
		var result ListResponse
		if err := decode(body, &result); err != nil {
			return nil, err
		}
		if result.Snapshots == nil || result.Count != len(result.Snapshots) {
			return nil, errors.New("invalid history list count")
		}
		b, err := graph.NewHistoryMarkerBuilder(q.ClusterID)
		if err != nil {
			return nil, err
		}
		for _, m := range result.Snapshots {
			if err := b.Add(ctx, m); err != nil {
				return nil, errors.New("history list scope or budget mismatch")
			}
		}
		value = result
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, errors.New("cannot encode history metadata")
	}
	if len(out)+1 > graph.MaxHistoryBytes {
		return nil, errors.New("encoded history metadata exceeds 2 MiB")
	}
	return append(out, '\n'), nil
}
func decode(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("history response does not match the supported metadata schema")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("history response contains trailing data")
	}
	return nil
}
func statusError(status int) error {
	hints := map[int]string{400: "invalid query; federated history requires --cluster", 401: "bearer authentication required (KUBEATLAS_TOKEN)", 403: "not authorized for this cluster", 404: "cluster not attached", 413: "history exceeds its budget; narrow the diff window", 429: "analysis capacity is busy", 503: "history is disabled or scoped reads are unsupported", 504: "history deadline exceeded"}
	hint := hints[status]
	if hint == "" {
		hint = "history read failed"
	}
	return fmt.Errorf("history server returned HTTP %d: %s", status, hint)
}
