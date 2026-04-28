# Contributing to KubeAtlas

We welcome contributions! By contributing, you agree to the
[Developer Certificate of Origin (DCO)](./DCO).

## How to contribute

1. Fork this repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Commit your changes with a sign-off (`git commit -s -m "Add my feature"`)
4. Push to your fork (`git push origin feature/my-feature`)
5. Open a Pull Request

The `-s` flag adds a `Signed-off-by` line, required under our DCO policy.

## Coding & commit conventions

KubeAtlas follows CNCF Sandbox-ready practices.

### Language

- Source code (identifiers, comments, godoc), commit messages, and PR/Issue titles MUST be in English.
- Documentation under `docs/` is English-canonical; translations under `docs/<locale>/` are welcome.

### Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

<optional body, wrapped at 72 chars>

Signed-off-by: Your Name <you@example.com>
```

Allowed `<type>` values: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `build`, `perf`.

Example:

```
feat(discovery): add informer-based watch loop

Replaces the dynamic-client polling path used in the PoC with a
SharedInformerFactory. Reduces incremental update latency from
~5s to <1s.

Signed-off-by: Random J Developer <random@developer.example.org>
```

### Code style

- **Go**: `gofmt`, `goimports`, and `golangci-lint` (config in `.golangci.yml`).
- **TypeScript / React**: `eslint` + `prettier` (config in `web/`).
- New code requires tests; CI enforces coverage non-regression.
