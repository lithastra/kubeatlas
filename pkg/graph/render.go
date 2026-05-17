// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ErrGraphvizNotFound is returned by the render functions when the
// Graphviz `dot` binary is not on PATH. The API layer maps it to a
// 503 rather than a 500 — the renderer degrades, the rest of the
// server keeps serving. See ADR 0012.
var ErrGraphvizNotFound = errors.New("graphviz 'dot' binary not found on PATH")

// renderFormats is the set of formats ToImage accepts — SVG and PNG
// only, per ADR 0012.
var renderFormats = map[string]bool{"svg": true, "png": true}

// ToSVG renders g to an SVG image. Shorthand for ToImage with "svg".
func ToSVG(ctx context.Context, g *Graph, opts DOTOptions) ([]byte, error) {
	return ToImage(ctx, g, "svg", opts)
}

// ToPNG renders g to a PNG image. Shorthand for ToImage with "png".
func ToPNG(ctx context.Context, g *Graph, opts DOTOptions) ([]byte, error) {
	return ToImage(ctx, g, "png", opts)
}

// ToImage renders g to an image by serialising it to DOT
// (ToDOTOptions) and piping that through the Graphviz `dot` CLI:
// DOT on stdin, the encoded image on stdout. No temporary files are
// used, so the renderer works on a read-only root filesystem.
//
// format must be "svg" or "png". When the `dot` binary is absent
// the error is ErrGraphvizNotFound; a non-zero `dot` exit is
// returned wrapped, with its stderr attached for diagnosis.
func ToImage(ctx context.Context, g *Graph, format string, opts DOTOptions) ([]byte, error) {
	if !renderFormats[format] {
		return nil, fmt.Errorf("unsupported render format %q (want svg or png)", format)
	}
	if _, err := exec.LookPath("dot"); err != nil {
		return nil, ErrGraphvizNotFound
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "dot", "-T"+format)
	cmd.Stdin = bytes.NewReader([]byte(ToDOTOptions(g, opts)))
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dot -T%s: %w: %s", format, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
