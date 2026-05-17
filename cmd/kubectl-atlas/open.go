// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"

	"github.com/pkg/browser"
)

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
