# Security Policy

## Supported versions

Only the latest stable KubeAtlas release receives security fixes. A new
stable release supersedes every older release; the project does not maintain
parallel patch branches or backport fixes to older versions.

## Reporting a vulnerability

**Please do not report security issues via public GitHub issues.**

Email dev@lithastra.com with:

- A description of the vulnerability
- Steps to reproduce
- The affected version (commit SHA if pre-release)
- Your proposed fix, if any

We will acknowledge a report within 7 calendar days. The acknowledgement will
state the next update date. Resolution time depends on severity and complexity;
we coordinate publication with the reporter instead of promising a fixed
deadline before assessment.

## Scope

In scope:

- KubeAtlas server (the `kubeatlas` binary)
- `kubectl-atlas` and offline collection/export paths
- Helm Chart in this repository
- Container images published to ghcr.io/lithastra/kubeatlas
- First-party rule packs and integrations when the issue originates in
  KubeAtlas-owned code or release configuration

Out of scope:

- Third-party dependencies (please report to the upstream project)
- Deliberately exposing the unauthenticated service contrary to the chart's
  warnings and acknowledgement gate. A bypass of those secure defaults remains
  in scope.
