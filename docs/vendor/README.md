# Maintained documentation security patches

These private, local packages patch the actual code behind two reviewed upstream
advisories. They are not upstream releases or registry publications. Upstream
package names remain unchanged; `-kubeatlas.1` identifies our local revision, not
an official patched version. A version label or a clean npm audit is not proof
that the source is safe.

| Local package | Published source | Advisory | Actual change |
| --- | --- | --- | --- |
| braces | 3.0.3 | [GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) | Bound parser nesting and all three recursive AST walkers, including caller-supplied ASTs. |
| http-cache-semantics | 4.2.0 | [GHSA-ch52-4w7c-c8xp](https://github.com/advisories/GHSA-ch52-4w7c-c8xp) | Require storage/revalidation permission before max-stale, stale-while-revalidate and stale-if-error reuse. |

## Source and licensing

`upstream-sources.json` records each published archive's npm SHA-512 integrity,
SHA-256 digest, exact original file hashes, changed files and patched hashes.
The source archives were checked against the original docs lockfile integrity.
Original source files, upstream README and LICENSE notices remain present.
braces is MIT; http-cache-semantics is BSD-2-Clause. Package metadata marks these
copies private, records their upstream identity and removes upstream development
tools/scripts; runtime dependencies are retained. We do not install the old
upstream build toolchains or publish these packages.

Direct `file:vendor/...` docs dependencies plus `$braces`/`$http-cache-semantics`
override references bind every transitive consumer to one reviewed local tree.
Consumer-path regressions walk the lockfile and require the real installed code
to resolve here, not merely a matching package name. This also avoids npm's
consumer-relative interpretation of bare local override paths.

npm audit does not independently assess unpublished local source. The existing
`npm audit --audit-level=low` gate stays unchanged and still assesses the rest
of the installed tree; these patches additionally require attack regressions,
source hashes, actual consumer resolution, semantic tests and manual code review.
No advisory ignore, lower threshold, fake upstream release or empty package is
used. Preserve the original failed main CI evidence rather than reclassifying it.

## Security and compatibility contract

The braces parser bounds its stack to 128 AST levels (at most 127 nested brace /
parenthesis containers plus root); every recursive AST walker has its own depth
check. The limit is fixed and cannot be raised by options. Excess depth produces
an explicit SyntaxError before call-stack exhaustion. Ordinary ranges, nested
alternatives, escaped/quoted literals and Docusaurus glob patterns are preserved.
Extremely deep formerly accepted patterns are intentionally rejected. Existing
range-expansion limits remain; this is not a claim of eliminating every possible
glob expansion/resource attack or protecting callers that deliberately disable
other limits.

The cache patch refuses unvalidated reuse of unstorable/no-cache/must-revalidate
entries and security-zeroed shared Set-Cookie responses lacking upstream's
explicit public/immutable opt-in. Shared proxy-revalidate/s-maxage entries cannot
be reused stale. The same check covers asynchronous and origin-error fallback,
including deserialized policies. Normal freshness, ordinary permitted stale
reuse, explicitly public cookies, private caches, Vary and conditional headers
keep their contract. Callers deliberately bypassing policy methods and directly
reading responseHeaders are outside this policy decision boundary.

Before patching, bounded tests reproduced an original RangeError for an 8001-byte
nested pattern below the upstream 10000-character cap, and unauthorized shared
Set-Cookie responses for both max-stale forms. Preserve red evidence outside Git.
The checked-in suite must fail if those vulnerable implementations return.

```sh
npm ci
npm audit --audit-level=low
npm run test:dependencies
npm run typecheck
npm run build
```

## Maintenance and removal

For each dependency update, review the two advisories and upstream releases,
compare our minimal source changes, regenerate source provenance only from
verified archives, and rerun all gates. Do not regenerate hashes to bless an
unreviewed change. Source/license hashes and attack tests are mandatory.

Once an official fix actually covers these attack paths: replace the local
package dependency and override with the verified supported release, prove all
consumers resolve to it, keep the attack/semantic regressions, then remove only
that obsolete vendored package and its manifest record in the same reviewed PR.
Adapt provenance tests explicitly when removing a local patch. Never remove the
patch first or assume a newer version alone repairs the behavior.

This documentation-only draft does not change runtime KubeAtlas images, public
artifact identities, production resources, the v1.7 soak contract or release
acceptance. Both advisories remain upstream-unpatched as last verified; local
security fixes are independently maintained until their removal is validated.
