# Releasing KubeAtlas

Generic step-by-step for cutting a tagged release. Replace `vX.Y.Z`
with the actual version (`v1.4.0`, `v1.3.1`, etc.) throughout.

Originally derived from a one-time `RELEASING-v1.3.0.md` that
covered the Phase 3 wrap-up; this file is the steady-state recipe
used for every subsequent release.

---

## 0. Pre-flight (must all be true before you tag)

Run from the repo root.

```bash
cd /home/nick/kubeatlas

# 0.1 Working tree clean, on main, in sync with origin
git fetch origin
git status -sb                       # ## main...origin/main, nothing dirty
git rev-parse --abbrev-ref HEAD      # main

# 0.2 Full test sweep
go test ./...
( cd web && npm run typecheck && npm run lint && npm test && npm run build )

# 0.3 Helm chart unit tests + API compatibility (frozen v1alpha1 surface)
helm unittest helm/kubeatlas
go run ./tools/api-compat-check

# 0.4 Helm chart version + appVersion match the tag you're about to push
grep -E "^(version|appVersion)" helm/kubeatlas/Chart.yaml
# Expect: version: X.Y.Z  /  appVersion: "X.Y.Z"   (no leading 'v')

# 0.5 CHANGELOG has a vX.Y.Z section (the release workflow extracts
#     it via `make changelog-extract VERSION=vX.Y.Z`)
make changelog-extract VERSION=vX.Y.Z && head -5 /tmp/release-notes.md

# 0.6 README + roadmap reflect vX.Y.Z as released
grep -n "vX.Y.Z" README.md docs/docs/intro.md docs/docs/roadmap.md | head

# 0.7 Versioned docs snapshot exists
test -d docs/versioned_docs/version-X.Y.Z && echo "snapshot OK"
```

Any failure → fix in `main` first; do not proceed. The release
workflow runs its own gates but does not undo a bad tag.

If 0.7 reports the snapshot is missing, cut it:

```bash
( cd docs && npx docusaurus docs:version X.Y.Z )
git add docs/versioned_docs docs/versioned_sidebars docs/versions.json
git commit -s -m "docs: snapshot vX.Y.Z"
```

---

## 1. Bump the Helm chart version

```bash
sed -i 's/^version:.*/version: X.Y.Z/'          helm/kubeatlas/Chart.yaml
sed -i 's/^appVersion:.*/appVersion: "X.Y.Z"/'  helm/kubeatlas/Chart.yaml
grep -E "^(version|appVersion)" helm/kubeatlas/Chart.yaml

git add helm/kubeatlas/Chart.yaml
git commit -s -m "chore: bump Helm chart to X.Y.Z for release"
git push origin main

# Wait for CI on main to go green before tagging
gh run watch --exit-status
```

---

## 2. Tag and push

```bash
# Annotated, signed tag. Drop -s if you don't have a GPG key.
git tag -s -a vX.Y.Z -m "vX.Y.Z — <headline>"
git push origin vX.Y.Z
```

`<headline>` matches the second half of the CHANGELOG header so the
GitHub release card title reads sensibly.

---

## 3. Wait for the release workflow

[.github/workflows/release.yml](.github/workflows/release.yml)
triggers on the tag push and runs:

1. `make changelog-extract VERSION=vX.Y.Z OUT=/tmp/release-notes.md`
   — pulls the matching CHANGELOG section.
2. `goreleaser release --clean --release-notes=/tmp/release-notes.md`
   — multi-platform binaries (`kubeatlas`, `kubectl-atlas`),
   checksums, SBOMs, cosign signatures, and a draft GitHub Release
   whose body is the extracted CHANGELOG section.
3. `docker buildx build --push` — multi-arch container image to
   `ghcr.io/lithastra/kubeatlas:X.Y.Z`, cosign-signed.
4. `helm package` + `helm push` — chart to
   `oci://ghcr.io/lithastra/charts/kubeatlas:X.Y.Z`, cosign-signed.

Watch + verify:

```bash
gh run watch --exit-status

# Assets exist
gh release view vX.Y.Z --json assets --jq '.assets[].name'

# Chart pulls anonymously (means the package is public + token endpoint works)
helm registry logout ghcr.io 2>/dev/null
rm -f ~/.config/helm/registry/config.json
helm pull oci://ghcr.io/lithastra/charts/kubeatlas --version X.Y.Z -d /tmp

# Image manifest is reachable
docker pull ghcr.io/lithastra/kubeatlas:X.Y.Z
docker inspect ghcr.io/lithastra/kubeatlas:X.Y.Z | jq '.[0].Config.Labels'
```

If any check fails: **do not delete the tag.** Investigate the run
log, fix forward in `main`, and either re-run failed jobs from the
Actions UI or cut a `vX.Y.Z+post1`-style patch tag.

If `helm pull` returns 403, the ghcr.io package is private —
flip both packages to public via the GitHub UI:

- `https://github.com/orgs/lithastra/packages/container/charts%2Fkubeatlas/settings`
- `https://github.com/orgs/lithastra/packages/container/kubeatlas/settings`

The chart and the image are independent packages.

---

## 4. Promote the draft release to public

Goreleaser leaves the release in **draft** state. Edit the title /
notes if needed (the body is auto-filled from the CHANGELOG via
`--release-notes`), then publish:

```bash
gh release view vX.Y.Z --web    # opens the editor
# Verify body looks right, hit "Publish release".
```

---

## 5. Smoke-test the published artifacts

In a throwaway namespace on a kind cluster:

```bash
kind create cluster --name release-smoke
kubectl create deploy nginx --image=nginx
kubectl expose deploy nginx --port=80
kubectl create configmap demo --from-literal=k=v
kubectl autoscale deploy nginx --min=2 --max=4 --cpu-percent=99

helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version X.Y.Z \
  --namespace kubeatlas --create-namespace
kubectl -n kubeatlas rollout status deploy/kubeatlas --timeout=120s
kubectl -n kubeatlas port-forward svc/kubeatlas 8080:80 >/tmp/pf.log 2>&1 &
PF_PID=$!
sleep 2

# API sanity
curl -fsS localhost:8080/healthz
curl -fsS localhost:8080/readyz
curl -s "localhost:8080/api/v1alpha1/graph?level=cluster" | jq '.nodes | length'
curl -s "localhost:8080/api/v1alpha1/resources/default/HorizontalPodAutoscaler/nginx" | jq '.outgoing'
#   ↑ should show one SCALES edge to default/Deployment/nginx

# UI sanity — open in browser
xdg-open http://localhost:8080 || open http://localhost:8080
# - Topology canvas renders
# - ⌘K palette finds "nginx"
# - Theme switcher cycles 5 themes
# - Resources page Kind column shows "HorizontalPodAutoscaler" in full
# - Click nginx Deployment → "↯ Show blast radius" highlights HPA + Service

# Tear down
kill $PF_PID
helm -n kubeatlas uninstall kubeatlas
kind delete cluster --name release-smoke
```

Anything broken → cut `vX.Y.(Z+1)` with the fix; don't try to
reissue `vX.Y.Z`.

---

## 6. Update the kubectl plugin index (krew)

The `kubectl atlas` plugin ships separately via krew-index. The
manifest is [plugins/atlas.yaml](plugins/atlas.yaml).

```bash
# 6.1 Compute SHA256 of every released kubectl-atlas asset
URL="https://github.com/lithastra/kubeatlas/releases/download/vX.Y.Z"
for os in linux darwin windows; do
  for arch in amd64 arm64; do
    file="kubectl-atlas_X.Y.Z_${os}_${arch}.tar.gz"
    sha=$(curl -fsSL "$URL/$file" | sha256sum | cut -d' ' -f1)
    printf "%-60s %s\n" "$file" "$sha"
  done
done | tee /tmp/vX.Y.Z-shas.txt

# 6.2 Edit plugins/atlas.yaml — replace every prior version with
#     vX.Y.Z and update every `sha256:` from /tmp/vX.Y.Z-shas.txt.
#     Six platforms (linux/darwin/windows × amd64/arm64).
${EDITOR:-vi} plugins/atlas.yaml

# 6.3 Validate locally
kubectl krew install --manifest=plugins/atlas.yaml
kubectl atlas --version             # should print "X.Y.Z (commit ..., built ...)"
kubectl krew uninstall atlas

# 6.4 Commit the updated manifest to kubeatlas
git add plugins/atlas.yaml
git commit -s -m "chore(krew): bump atlas plugin manifest to vX.Y.Z"
git push origin main

# 6.5 Open the upstream krew-index PR
mkdir -p /tmp/krew-work && cd /tmp/krew-work
git clone https://github.com/kubernetes-sigs/krew-index || (cd krew-index && git pull)
cd krew-index
git checkout -b atlas-vX.Y.Z
cp /home/nick/kubeatlas/plugins/atlas.yaml plugins/atlas.yaml
git add plugins/atlas.yaml
git commit -s -m "atlas: upgrade to vX.Y.Z" \
  -m "Bumps URIs and sha256 across all six platforms." \
  -m "Release notes: https://github.com/lithastra/kubeatlas/releases/tag/vX.Y.Z"
gh repo set-default kubernetes-sigs/krew-index
gh pr create --title "atlas: upgrade to vX.Y.Z" \
  --body "Upgrades the atlas plugin to vX.Y.Z. SHAs verified locally with kubectl krew install --manifest."
cd /home/nick/kubeatlas
```

Krew-index PRs merge on the maintainers' cadence (often days);
the GitHub release is already public so users can still
`curl -L .../kubectl-atlas_X.Y.Z_*.tar.gz`.

---

## 7. Publish the docs site

```bash
cd /home/nick/kubeatlas/docs
npm install
npm run build
ls build/versions/             # confirm X.Y.Z is there

# Push to your hosting (pick the matching path):
#   GitHub Pages via a workflow:  on tag push the docs workflow
#     deploys; just verify https://docs.kubeatlas.lithastra.com.
#   Manual / Cloudflare Pages:    wrangler pages deploy build
cd /home/nick/kubeatlas
```

---

## 8. Post-release housekeeping

```bash
# 8.1 Add an Unreleased buffer back to CHANGELOG (if not still present)
${EDITOR:-vi} CHANGELOG.md
#     Insert above "## [vX.Y.Z]":
#       ## [Unreleased]
#
#       ### Added / Changed / Fixed
#
#       _(none yet)_

git add CHANGELOG.md
git commit -s -m "chore(changelog): open Unreleased section after vX.Y.Z"
git push origin main

# 8.2 File any follow-up issues for known polish items.
# Use the project's vX.Y.x label so they sort with the line they target.
```

---

## Rollback checklist (if something is on fire after release)

1. **Don't delete the tag or the GitHub release.** Operators may
   have pinned `X.Y.Z` already.
2. **Cut a `vX.Y.(Z+1)` patch.** Even a chart-only fix bumps the
   patch.
3. **Add a deprecation note** to the bad release's GitHub page if
   the bug is severe enough to recommend skipping.

---

## Why each step exists

- **0.5 / 3.1** — `make changelog-extract` populates the GitHub
  release body from `CHANGELOG.md` automatically. Without it every
  release card carries the same static header (the
  Phase 1 boilerplate problem that hit v0.1.0 through v1.3.0).
- **3 + 5** — separating "workflow finished" from "smoke tested"
  keeps the `gh release publish` step gated on a real e2e check, not
  just "CI green".
- **6** — krew-index is a separate Kubernetes-SIG repo; it doesn't
  watch our releases, so the PR is manual every time.
- **8.1** — leaves the next release's section pre-opened so the
  first PR after the cut doesn't have to wonder where to add notes.
