// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"strings"

	"github.com/pkg/browser"
)

func init() {
	// xdg-open and its kin are noisy on a headless host: each browser
	// they fail to find prints a "not found" line. We already detect
	// failure from the exit status (OpenURL/OpenFile return an error)
	// and fall back to printing the URL ourselves, so silence the
	// child process's streams.
	browser.Stdout = io.Discard
	browser.Stderr = io.Discard
}

// opener opens a URL or a local file. It is a function type, not a
// direct call into github.com/pkg/browser, so the command tests can
// substitute a fake that just records the argument — CI has no
// display to launch a real browser into.
type opener func(target string) error

// systemBrowser is the production opener. A target with a "://"
// scheme is opened as a URL (online mode's UI deep-link); anything
// else is treated as a local file path (offline mode's rendered
// SVG). github.com/pkg/browser is pure Go and picks the right
// per-OS mechanism for each.
func systemBrowser(target string) error {
	if strings.Contains(target, "://") {
		return browser.OpenURL(target)
	}
	return browser.OpenFile(target)
}
