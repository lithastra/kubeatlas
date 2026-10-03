// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestImpactEncodingExactBudgetAndStandardJSON(t *testing.T) {
	for _, name := range []string{"plain", "\x00\t\n\r\b\f<&>\"\\", "\u65e5\u672c\u8a9e\U0001f600\u2028\u2029", string([]byte{0xff, 0xfe}), ""} {
		t.Run(name, func(t *testing.T) {
			response := ImpactResponse{SchemaVersion: "1", GeneratedAt: time.Now(), Analysis: &analysis.ImpactResult{
				Root: analysis.ImpactIdentity{Name: name}, Resources: []analysis.ImpactMatch{},
				Authorization: analysis.ImpactAuthorizationResult{Resources: []analysis.ImpactAuthorizationMatch{{
					Path: []analysis.ImpactAuthorizationStep{{ImpactStep: analysis.ImpactStep{From: name, To: "role"}, BindingScope: "cluster"}},
				}}},
			}}
			want, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			for _, extra := range []int{-1, 0, 1} {
				got, err := encodeImpactResponse(context.Background(), response, len(want)+extra)
				if extra < 0 {
					if !errors.Is(err, errImpactResponseLimit) || got != nil {
						t.Fatal("accepted undersized cap")
					}
					continue
				}
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("wire shape drift: %v", err)
				}
			}
		})
	}
	// The cap includes the envelope, BOTH facets, and every explanation, not
	// independent per-facet caps. Use the actual 2 MiB limit at its exact edge.
	r := ImpactResponse{Analysis: &analysis.ImpactResult{}}
	base, _ := json.Marshal(r)
	r.SharingWarning = strings.Repeat("x", impactResponseBytes-len(base))
	got, err := encodeImpactResponse(context.Background(), r, impactResponseBytes)
	if err != nil || len(got) != impactResponseBytes {
		t.Fatalf("exact 2 MiB boundary: len=%d err=%v", len(got), err)
	}
	r.Analysis.Authorization.Reason = "x"
	if got, err := encodeImpactResponse(context.Background(), r, impactResponseBytes); !errors.Is(err, errImpactResponseLimit) || got != nil {
		t.Fatal("authorization facet escaped combined cap")
	}
}

type impactCancelAfterChecks struct {
	context.Context
	remaining atomic.Int64
}

func (c *impactCancelAfterChecks) Err() error {
	if c.remaining.Add(-1) <= 0 {
		return context.Canceled
	}
	return nil
}

func TestImpactEncodingCancellationAndUnsupportedPayload(t *testing.T) {
	ctx := &impactCancelAfterChecks{Context: context.Background()}
	ctx.remaining.Store(10)
	r := ImpactResponse{Analysis: &analysis.ImpactResult{Resources: make([]analysis.ImpactMatch, 100)}}
	if body, err := encodeImpactResponse(ctx, r, impactResponseBytes); !errors.Is(err, context.Canceled) || body != nil {
		t.Fatal("encoding ignored mid-walk cancellation")
	}
	budget := impactResponseBytes
	if err := measureImpactJSON(context.Background(), reflect.ValueOf(map[string]any{"raw": "synthetic-canary"}), &budget); !errors.Is(err, errImpactEncoding) {
		t.Fatal("encoder admitted arbitrary object data")
	}
}

func TestImpactOpenAPISchemaAndAlphaIsolation(t *testing.T) {
	s := New("", memory.New(), aggregator.NewRegistry())
	alpha := s.OpenAPISpecV1Alpha1()
	v1 := s.OpenAPISpecV1()
	alphaJSON, err := json.Marshal(alpha)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(alphaJSON), "ImpactResponse") || strings.Contains(string(alphaJSON), "/impact/") || strings.Contains(string(alphaJSON), "ImpactErrorResponse") {
		t.Fatal("v1-only contract leaked into alpha spec")
	}
	paths := v1["paths"].(map[string]any)
	op := paths[impactPath].(map[string]any)["get"].(map[string]any)
	responses := op["responses"].(map[string]any)
	for _, code := range []string{"200", "400", "401", "403", "404", "409", "413", "429", "500", "503", "504"} {
		if responses[code] == nil {
			t.Fatalf("undocumented status %s", code)
		}
	}
	props := impactResponseSchema()["properties"].(map[string]any)
	for _, key := range []string{"schemaVersion", "scope", "analysis", "sharingWarning", "generatedAt", "kubeatlasVersion"} {
		if props[key] == nil {
			t.Fatalf("undocumented envelope field %s", key)
		}
	}
	analysisProps := props["analysis"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"root", "resources", "counts", "referenceEvidence", "authorization", "observation", "availability", "truncated", "truncationReasons"} {
		if analysisProps[key] == nil {
			t.Fatalf("undocumented result field %s", key)
		}
	}
	// Embedded ImpactStep stays flattened, not a synthetic nested field.
	step := impactSchemaFor(reflect.TypeFor[analysis.ImpactAuthorizationStep]())["properties"].(map[string]any)
	if step["ImpactStep"] != nil || step["from"] == nil || step["bindingScope"] == nil {
		t.Fatal("embedded step schema drift")
	}
}

type impactDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *impactDeadlineWriter) SetWriteDeadline(d time.Time) error {
	w.deadlines = append(w.deadlines, d)
	return nil
}

func TestImpactWriteDeadlineTraversesMiddleware(t *testing.T) {
	base := &impactDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	w := &statusRecorder{ResponseWriter: &statusRecorder{ResponseWriter: base}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	writeImpactReply(w, ctx, impactFailure(http.StatusForbidden, "forbidden", "denied"))
	if len(base.deadlines) != 2 || !base.deadlines[0].Equal(deadline) || !base.deadlines[1].IsZero() || base.Code != 403 {
		t.Fatal("socket deadline was lost or keep-alive state not reset")
	}
}

type impactPipeWriter struct {
	conn   net.Conn
	header http.Header
	err    error
}

func (w *impactPipeWriter) Header() http.Header                { return w.header }
func (w *impactPipeWriter) WriteHeader(int)                    {}
func (w *impactPipeWriter) SetWriteDeadline(d time.Time) error { return w.conn.SetWriteDeadline(d) }
func (w *impactPipeWriter) Write(p []byte) (int, error) {
	n, err := w.conn.Write(p)
	w.err = err
	return n, err
}

func TestImpactSlowSocketWriteIsDeadlineBound(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	writer := &impactPipeWriter{conn: server, header: make(http.Header)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { writeImpactReply(writer, ctx, impactReply{status: 200, body: []byte("unread")}); close(done) }()
	// The peer intentionally never reads: the real net.Conn write must time out.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow socket exceeded its deadline")
	}
	// Socket and context timers are independent; assert the actual write error,
	// not which timer's callback happened to run first.
	if !errors.Is(writer.err, os.ErrDeadlineExceeded) {
		t.Fatalf("write did not encounter the deadline: %v", writer.err)
	}
}
