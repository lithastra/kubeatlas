// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"
)

// With an empty PATH the kubeatlas binary cannot be found, so
// renderOffline must fail with a clear, actionable message rather
// than a bare exec error.
func TestRenderOffline_KubeatlasNotOnPath(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := renderOffline(context.Background(), "", kubeFlags{})
	if err == nil {
		t.Fatal("expected an error when kubeatlas is not on PATH")
	}
	if !strings.Contains(err.Error(), "kubeatlas") {
		t.Errorf("error %q should name the missing kubeatlas binary", err)
	}
}
