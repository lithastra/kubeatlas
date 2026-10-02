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
