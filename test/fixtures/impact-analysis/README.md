# Impact client contract matrix

These portable synthetic fixtures are shared by Go API, CLI, and Web tests.
They are not captures from a user's cluster or release acceptance evidence.
No Kubernetes credentials, Secret objects/values, or workstation paths are
required. The Secret in the dependency path is only an edge endpoint.

- `client-matrix.json` contains a hand-authored graph, queries, and expected
  ordinary/authorization resource identities, direct counts, and truncation.
- `client-captures.json` contains the complete v1 responses for those queries.
  The API test seeds the real memory store and runs the actual handler/engine,
  checks the handwritten expectations, and compares the whole response.
  Only the server version and capture timestamps are fixed; identity, paths,
  counts, coverage, reference evidence, and limits are not normalized away.
- The CLI command tests consume those responses in text, JSON, and HTML.
- The Web integration tests substitute only HTTP transport and the browser's
  download sink. The real response reader, query hook, panel, and exporters
  consume the same captures, including expanded paths and per-capture consent.

Run from the repository root:

```bash
go test -short -race ./pkg/api ./cmd/kubectl-atlas -run 'TestImpact(ClientMatrixWireContract|CommandClientMatrix)' -count=1
npm --prefix web test -- --runInBand ImpactClientMatrix
```

The six cases cover cross-namespace dependents, dependencies with a
reference-only Secret, depth truncation, incomplete empty results, independent
authorization associations, and one authorized federated member. All use
unknown observation coverage intentionally; none proves absence or safety.

When the contract changes, review the graph/oracles and complete response
differences together with all three consumers. Do not blindly regenerate
captures to silence a failing test. These fixtures do not replace browser
layout checks, real-environment authorization checks, or candidate validation.

## Production browser integration

`web/tests/e2e/impact.spec.ts` reuses this matrix in Chromium against the Vite
production bundle. API responses and the WebSocket transport are substituted;
the real routes, query identity checks, controls, paths, and browser downloads
run. Five standalone scenarios and the federated east scenario check displayed
evidence and captures. Boundary checks cover refresh/loading followed by a 403,
response identity mismatch, query changes and consent reset, incomplete/truncated
qualification, narrow resource layout, and cluster switches clearing exports
even when an old refresh completes late. West denial is a synthetic 403, not
proof of deployed server authorization. Browser replies add whitespace and a
final newline to the unchanged fixture semantics, so export comparisons also
detect reserialization. HTML must retain its CSP and contain no active or
external-resource elements.

```bash
npm --prefix web run build
npm --prefix web run typecheck:e2e
# Install the lockfile-matched Chromium once if it is not already available.
cd web && npx playwright install chromium && npm run test:impact-browser
```

CI runs the production build and this single-worker suite as a separate job.
The default Playwright configuration retains the live PetClinic smoke suite;
the impact configuration starts only a loopback production preview and needs
no API server, Kubernetes credentials, cluster, or kubectl. Synthetic browser
integration does not establish real-cluster watch continuity, deployed
authentication, effective permissions, frozen-candidate performance, or M4
acceptance. The Go/CLI contract tests remain independent checks of the fixture.

## Live Go HTTP, production Web and actual CLI

`web/tests/e2e/impact-live.spec.ts` runs all six cases against the real Go API
serving the built Web assets, with no route fulfillment or WebSocket replacement.
The compiled CLI connects to the same server in that round. Web JSON and HTML
retain the received HTTP text exactly. CLI JSON/HTML must match the complete
Web evidence after only six capture clock fields are normalized. Server impact
counters verify downloads add no request and each CLI report adds exactly one.
Real `RBACScope` rules permit east and deny west; missing/unknown tokens, closed
Web refresh errors, absent CLI output on denial and 0600 report files are checked.

```bash
npm --prefix web run build
go test -buildvcs=false -c -o bin/impact-browser-server.test ./pkg/api
CGO_ENABLED=0 go build -buildvcs=false -o bin/kubectl-atlas ./cmd/kubectl-atlas
npm --prefix web run test:impact-live
```

The runner is an opt-in Go test, skipped by ordinary `go test`. It reuses the
same matrix seed, binds only `127.0.0.1:4174` and `127.0.0.1:4175`, stops on
signals, and fails after 90 seconds if not stopped. Playwright owns its lifecycle.
No informer, Kubernetes configuration, database or user token is loaded by the
runner. Credentials, rules and graph data are synthetic. The real WebSocket
connects to the Go hub, but no watch events/continuity are established. These
seven live tests close the repeatable client/API connection gap while M4's
frozen deployment, real collector/authorization setup and endurance gates remain.
