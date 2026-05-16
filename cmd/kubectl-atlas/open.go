// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "github.com/pkg/browser"

// opener opens a URL in the operator's default browser. It is a
// function type, not a direct call to github.com/pkg/browser, so the
// command tests can substitute a fake that just records the URL —
// CI has no display to launch a real browser into.
type opener func(targetURL string) error

// systemBrowser is the production opener: github.com/pkg/browser is
// pure Go and picks the right mechanism per OS (open / xdg-open /
// rundll32).
func systemBrowser(targetURL string) error {
	return browser.OpenURL(targetURL)
}
