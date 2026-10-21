// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

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
	"unicode/utf8"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

const MaxTriggerReceiptBytes = 4096
const TriggerTimeout = 30 * time.Second

// TriggerQuery selects one internal operator write, never a member fan-out.
// Read visibility does not grant operator access to the internal route.
type TriggerQuery struct {
	ClusterID string
	Trigger   graph.SnapshotTrigger
}

type TriggerReceipt struct {
	ClusterID     string                `json:"clusterId,omitempty"`
	Trigger       graph.SnapshotTrigger `json:"trigger"`
	ResourceCount int64                 `json:"resourceCount"`
	EdgeCount     int64                 `json:"edgeCount"`
	DurationMS    int64                 `json:"durationMs"`
}

// TriggerStatusError preserves the legacy CLI's non-200 exit status without
// echoing provider bodies, credentials, URLs or untrusted error text.
type TriggerStatusError struct{ Status int }

func (e *TriggerStatusError) Error() string {
	return fmt.Sprintf("marker server returned HTTP %d; write outcome may be unknown; no automatic retry", e.Status)
}

func (q TriggerQuery) requestURL(base string) (string, error) {
	if q.ClusterID != "" && !validSegment(q.ClusterID) || !graph.ValidSnapshotMarker(q.ClusterID, q.Trigger) {
		return "", errors.New("select one exact member and manual or periodic trigger")
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("use an HTTP(S) server base URL without credentials, query or fragment")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if (ip == nil || !ip.IsLoopback()) && !internalServiceHost(u.Hostname()) {
			return "", errors.New("HTTP requires a literal loopback or service.namespace.svc address; other marker servers require HTTPS")
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", errors.New("invalid server URL path")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/_internal/snapshot/trigger"
	u.RawPath = ""
	v := url.Values{"trigger": {string(q.Trigger)}}
	if q.ClusterID != "" {
		v.Set("cluster", q.ClusterID)
	}
	u.RawQuery = v.Encode()
	return u.String(), nil
}

// This is a syntactic exception for the existing in-cluster HTTP CronJob path,
// not proof of pod location, DNS authenticity, TLS or network isolation. The
// operator must trust that internal path before providing a bearer credential.
func internalServiceHost(host string) bool {
	parts := strings.Split(host, ".")
	short := len(parts) == 3 && parts[2] == "svc"
	full := len(parts) == 5 && parts[2] == "svc" && parts[3] == "cluster" && parts[4] == "local"
	if !short && !full {
		return false
	}
	for _, label := range parts[:2] {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			valid := ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-'
			if !valid {
				return false
			}
		}
	}
	return true
}

// Trigger makes one POST to an operator-provided internal endpoint. No retries,
// redirects, environment proxy, API fallback, discovery or Kubernetes writes.
// Rejected/missing receipts and transport/output errors cannot undo the marker;
// repeating the operation is a separate write, not an idempotent recovery step.
func Trigger(ctx context.Context, base, token string, q TriggerQuery) ([]byte, error) {
	endpoint, err := q.requestURL(base)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid bearer token")
	}
	ctx, cancel := context.WithTimeout(ctx, TriggerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, http.NoBody)
	if err != nil {
		return nil, errors.New("cannot construct marker request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// A fresh non-reusing transport also avoids replaying a failed reused
	// connection. Proxy remains nil; TLS uses the system's normal verification.
	transport := &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("marker request failed; write outcome may be unknown; no automatic retry")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &TriggerStatusError{Status: resp.StatusCode}
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, invalidTriggerReceipt()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxTriggerReceiptBytes+1))
	if err != nil || len(body) > MaxTriggerReceiptBytes || ctx.Err() != nil {
		return nil, invalidTriggerReceipt()
	}
	receipt, err := validateTriggerReceipt(body, q)
	if err != nil {
		return nil, invalidTriggerReceipt()
	}
	out, err := json.Marshal(receipt)
	if err != nil || ctx.Err() != nil {
		return nil, invalidTriggerReceipt()
	}
	return append(out, '\n'), nil
}

func invalidTriggerReceipt() error {
	return errors.New("marker receipt is unavailable or invalid; write outcome may be unknown; no automatic retry")
}
func validateTriggerReceipt(body []byte, q TriggerQuery) (TriggerReceipt, error) {
	if !utf8.Valid(body) {
		return TriggerReceipt{}, invalidTriggerReceipt()
	}
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return TriggerReceipt{}, invalidTriggerReceipt()
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return TriggerReceipt{}, invalidTriggerReceipt()
		}
		// encoding/json's struct matching is case-insensitive. Enforce the
		// exact wire keys here so aliases cannot overwrite a validated field.
		switch name {
		case "clusterId", "trigger", "resourceCount", "edgeCount", "durationMs":
		default:
			return TriggerReceipt{}, invalidTriggerReceipt()
		}
		seen[name] = true
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return TriggerReceipt{}, invalidTriggerReceipt()
		}
	}
	if _, err := d.Token(); err != nil {
		return TriggerReceipt{}, invalidTriggerReceipt()
	}
	var wire struct {
		ClusterID     *string                `json:"clusterId"`
		Trigger       *graph.SnapshotTrigger `json:"trigger"`
		ResourceCount *int64                 `json:"resourceCount"`
		EdgeCount     *int64                 `json:"edgeCount"`
		DurationMS    *int64                 `json:"durationMs"`
	}
	if decode(body, &wire) != nil || wire.Trigger == nil || wire.ResourceCount == nil || wire.EdgeCount == nil || wire.DurationMS == nil || seen["clusterId"] && wire.ClusterID == nil {
		return TriggerReceipt{}, invalidTriggerReceipt()
	}
	cluster := ""
	if wire.ClusterID != nil {
		cluster = *wire.ClusterID
	}
	if cluster != q.ClusterID || *wire.Trigger != q.Trigger || *wire.ResourceCount < 0 || *wire.EdgeCount < 0 || *wire.DurationMS < 0 {
		return TriggerReceipt{}, invalidTriggerReceipt()
	}
	return TriggerReceipt{ClusterID: cluster, Trigger: *wire.Trigger, ResourceCount: *wire.ResourceCount, EdgeCount: *wire.EdgeCount, DurationMS: *wire.DurationMS}, nil
}
