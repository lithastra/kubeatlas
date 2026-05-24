# Releasing KubeAtlas v1.3.0

One-time guide for tagging and shipping v1.3.0 — the third and
final Phase 3 release (multi-cluster federation, platform-identity
edges, HorizontalPodAutoscaler support, cartography Web UI
redesign).

Once v1.3.0 is out, archive this file or fold it into a generic
`RELEASING.md` for v1.4.

---

## 0. Pre-flight (must all be true before you tag)

Run every check below from the repo root.

```bash
# 0.1 Working tree is clean, on main, in sync with origin
git status                          # nothing to commit
git rev-parse --abbrev-ref HEAD     # main
git fetch origin && git status -sb  # ## main...origin/main

# 0.2 Full test sweep
go test ./...
( cd web && npm run typecheck && npm run lint && npm test && npm run build )

# 0.3 API compatibility check (frozen v1alpha1 surface)
go run ./tools/api-compat-check

# 0.4 Docs versioned for v1.3.0
test -d docs/versioned_docs/version-1.3.0 || \
  ( cd docs && npx docusaurus docs:version 1.3.0 )

# 0.5 Helm chart version + appVersion match the tag
grep -E "^(version|appVersion)" helm/kubeatlas/Chart.yaml
# Expect both to be 1.3.0 (or 1.3.0-rc.N during pre-release)

# 0.6 CHANGELOG has a v1.3.0 entry
grep -n "^## \[v1.3.0\]" CHANGELOG.md

# 0.7 README + roadmap reflect v1.3.0 as released
grep -n "v1.3.0" README.md docs/docs/intro.md docs/docs/roadmap.md | head
```

Any failure → fix and re-run from 0.1. The release workflow runs
its own gates but does not undo a bad tag, so it's cheaper to
catch problems here.

---

## 1. Bump the Helm chart version

```bash
# Replace whatever is in Chart.yaml with the release version
sed -i 's/^version:.*/version: 1.3.0/'          helm/kubeatlas/Chart.yaml
sed -i 's/^appVersion:.*/appVersion: "1.3.0"/'  helm/kubeatlas/Chart.yaml
grep -E "^(version|appVersion)" helm/kubeatlas/Chart.yaml

git add helm/kubeatlas/Chart.yaml
git commit -s -m "chore: bump Helm chart to 1.3.0 for release"
git push origin main
```

Wait for CI on main to go green before tagging — if CI fails, the
release workflow will fail too. Watch:
`gh run watch --exit-status` or [Actions](https://github.com/lithastra/kubeatlas/actions).

---

## 2. Tag and push

```bash
# Annotated tag with a short message; the release workflow keys on it
git tag -s -a v1.3.0 -m "v1.3.0 — multi-cluster federation, cartography UI"
git push origin v1.3.0
```

If you skip the GPG signature (`-s`), drop the flag. Don't push
the tag without pushing the chart-bump commit first.

---

## 3. Wait for the release workflow

[.github/workflows/release.yml](.github/workflows/release.yml)
triggers on tag push and runs:

1. `go test ./...` + the web build.
2. `goreleaser release` — multi-platform binaries for `kubeatlas`
   and `kubectl-atlas` published as a GitHub Release with SBOMs
   and cosign signatures.
3. `docker buildx build --push` — multi-arch container image to
   `ghcr.io/lithastra/kubeatlas:1.3.0`, cosign-signed.
4. `helm package` + `helm push` — the chart to
   `oci://ghcr.io/lithastra/charts/kubeatlas:1.3.0`, also
   cosign-signed.

Watch and verify:

```bash
gh run watch --exit-status

# Assets exist where they should
gh release view v1.3.0
crane manifest ghcr.io/lithastra/kubeatlas:1.3.0 | jq '.config'
helm pull oci://ghcr.io/lithastra/charts/kubeatlas --version 1.3.0
```

If any step fails, **do not delete the tag**. Investigate the run
log, fix forward in main, and either re-run failed jobs from the
Actions UI or push a `v1.3.0+post1`-style patch tag.

---

## 4. Smoke-test the published artifacts

In a throwaway namespace on a kind cluster you don't mind
destroying:

```bash
kind create cluster --name release-smoke
kubectl create deploy nginx --image=nginx
kubectl expose deploy nginx --port=80
kubectl create configmap demo --from-literal=k=v

helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.3.0 \
  --namespace kubeatlas --create-namespace
kubectl -n kubeatlas rollout status deploy/kubeatlas --timeout=120s
kubectl -n kubeatlas port-forward svc/kubeatlas 8080:80 &

# UI sanity
open http://localhost:8080
# Walk: Topology canvas renders, ⌘K palette finds nginx, theme
# switcher cycles 5 themes, blast-radius button works, Resources
# page table autosizes.

# API sanity
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
curl -s "localhost:8080/api/v1alpha1/graph?level=cluster" | jq '.nodes | length'

# Federation sanity (404 expected on single-cluster install)
curl -sw "%{http_code}\n" localhost:8080/api/v1/federation/clusters

# Tear down
helm -n kubeatlas uninstall kubeatlas
kind delete cluster --name release-smoke
```

Anything broken → cut a `v1.3.1` patch with the fix; don't try to
reissue `v1.3.0`.

---

## 5. Update the kubectl plugin index (krew)

The `kubectl atlas` plugin ships separately from the binary release
via the krew-index. The manifest is in
[plugins/atlas.yaml](plugins/atlas.yaml).

```bash
# 5.1 Compute the new SHAs from the GitHub release assets
URL="https://github.com/lithastra/kubeatlas/releases/download/v1.3.0"
for os in linux darwin windows; do
  for arch in amd64 arm64; do
    [ "$os/$arch" = "windows/arm64" ] && continue
    file="kubectl-atlas_${os}_${arch}.tar.gz"
    [ "$os" = "windows" ] && file="kubectl-atlas_${os}_${arch}.zip"
    printf "%-40s %s\n" "$file" "$(curl -sL "$URL/$file" | sha256sum | cut -d' ' -f1)"
  done
done

# 5.2 Update plugins/atlas.yaml with the new version, URIs, and SHAs
# (edit by hand; the schema is in
#  https://krew.sigs.k8s.io/docs/developer-guide/plugin-manifest/)

# 5.3 Validate locally
kubectl krew install --manifest=plugins/atlas.yaml
kubectl krew uninstall atlas

# 5.4 Open a PR against kubernetes-sigs/krew-index
git -C /tmp clone https://github.com/kubernetes-sigs/krew-index || true
cp plugins/atlas.yaml /tmp/krew-index/plugins/atlas.yaml
cd /tmp/krew-index
git checkout -b kubeatlas-v1.3.0
git add plugins/atlas.yaml
git commit -s -m "atlas: upgrade to v1.3.0"
gh pr create --title "atlas: upgrade to v1.3.0" --body "..."
```

Merging into krew-index can take days — that's fine, the GitHub
release is already public.

---

## 6. Publish the docs

```bash
cd docs
npm install
npm run build      # confirm the v1.3.0 version is in dist/versions/
```

Then push to whatever hosts `docs.kubeatlas.lithastra.com` (Pages
site, Cloudflare Pages, etc. — depends on your hosting). The
versioned snapshot you cut in step 0.4 is what gets served as
`/docs/version-1.3.0/`.

---

## 7. Announce

- GitHub release notes: auto-generated by goreleaser, but paste the
  CHANGELOG `## [v1.3.0]` section over the top.
- Blog post / social: the headline is "Phase 3 is complete:
  multi-cluster federation, platform identity, and a brand-new
  cartography UI".
- Update the project status badge in any README / website you
  control.

---

## Rollback checklist (if something is on fire after release)

1. **Don't delete the tag or the GitHub release.** Operators may
   have pinned `1.3.0` already.
2. **Cut a `v1.3.1` patch.** Even a chart-only fix bumps the patch.
3. **Add a deprecation note** to the v1.3.0 GitHub release if the
   bug is severe enough to recommend skipping the release.

---

## Post-release todo

- [ ] Add `## [Unreleased]` placeholder back to `CHANGELOG.md`.
- [ ] Open follow-up issues for the v1.3.x polish items called out
      in the roadmap (cluster picker → `/federation/graph` wiring,
      FLIP zoom, drag-anchor time scrub, full M6 a11y sweep).
- [ ] Archive or fold this file into a generic `RELEASING.md` so
      the next release isn't a fresh ad-hoc.
