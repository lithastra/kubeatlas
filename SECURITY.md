# Security Policy

## Supported versions

KubeAtlas is in pre-alpha (Phase 0). No versions are formally supported
for security updates yet. The project will define a support policy at
v1.0 release.

## Reporting a vulnerability

**Please do not report security issues via public GitHub issues.**

Email dev@lithastra.com with:

- A description of the vulnerability
- Steps to reproduce
- The affected version (commit SHA if pre-release)
- Your proposed fix, if any

We will acknowledge your report within 48 hours and provide a more
detailed response within 5 business days indicating the next steps in
handling your report.

## Scope

In scope:

- KubeAtlas server (the `kubeatlas` binary)
- Helm Chart in this repository (when published)
- Container images published to ghcr.io/lithastra/kubeatlas

Out of scope:

- Third-party dependencies (please report to the upstream project)
- User misconfigurations (e.g., exposing the service to the public
  internet without an authentication layer)