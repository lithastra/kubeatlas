// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"html/template"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

//go:embed templates/diagnose.html
var diagnoseTemplateSource string

// diagnoseTemplate is parsed once at init. A parse failure is a
// programming error (the template is embedded at compile time), so
// template.Must is the right call.
var diagnoseTemplate = template.Must(template.New("diagnose").Parse(diagnoseTemplateSource))

// diagnoseView is the data the HTML template renders. It wraps the
// report with the pre-rendered (and trusted) graph SVG plus a
// fallback message used when the graph could not be rendered.
type diagnoseView struct {
	Report       *DiagnoseReport
	GeneratedAt  string
	GraphSVG     template.HTML
	GraphMessage string
}

// RenderHTML renders a DiagnoseReport to a single self-contained HTML
// document: inline CSS, no external resources, dark-mode aware. It is
// the human-facing counterpart to marshalling the report as JSON.
//
// The dependency graph is rendered to inline SVG via the graphviz
// 'dot' binary. When dot is absent (a common air-gapped case) the
// report still renders in full — every other section is independent of
// graphviz — with a notice in place of the image, so the report is
// never empty just because the host lacks graphviz.
func RenderHTML(ctx context.Context, report *DiagnoseReport) ([]byte, error) {
	view := diagnoseView{
		Report:      report,
		GeneratedAt: report.GeneratedAt.UTC().Format(time.RFC3339),
	}

	svg, err := graph.ToSVG(ctx, report.Graph, graph.DOTOptions{
		Namespace: report.Scope.namespaceFilter(),
		Title:     "KubeAtlas",
	})
	switch {
	case err == nil:
		// The SVG is produced by our own graphviz renderer over our
		// own graph data; graphviz XML-escapes node labels, so it is
		// safe to embed verbatim rather than re-escape it as text.
		view.GraphSVG = template.HTML(svg) //nolint:gosec // trusted, self-rendered SVG
	case errors.Is(err, graph.ErrGraphvizNotFound):
		view.GraphMessage = "Graph image unavailable: the graphviz 'dot' binary was not found on PATH. " +
			"Install graphviz to embed the rendered dependency graph; every other section of this report is complete."
	default:
		return nil, err
	}

	var buf bytes.Buffer
	if err := diagnoseTemplate.Execute(&buf, view); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
