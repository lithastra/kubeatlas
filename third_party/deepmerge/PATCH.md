# Local deepmerge security patch

This private copy of deepmerge 4.3.1 addresses GHSA-gcqx-w48m-w2h4.
Both distributed entry points write own data properties with
`Object.defineProperty`. A supplied `__proto__` property cannot invoke an
inherited setter on the returned object. Existing key filtering, recursive
merge behavior, custom mergers, symbols and array handling are preserved.

The upstream license and package identity are retained. `upstream.json` records
the original tarball and source hashes. The local version suffix identifies
this maintained patch. npm audit does not assess this local source, so the
regression tests run independently alongside the normal dependency audit.

Run `node --test third_party/deepmerge/security.test.cjs` from the repository root.
