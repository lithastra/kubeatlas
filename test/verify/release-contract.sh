#!/usr/bin/env bash

# Verify the metadata and immutable dependency contract shared by release
# preflight and the publishing workflow. This script performs no network writes
# and creates only the requested release-notes file.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
RELEASE_TAG=${1:-}
NOTES_OUT=${2:-${RUNNER_TEMP:-/tmp}/kubeatlas-release-notes.md}

fail() {
  printf 'release contract: %s\n' "$*" >&2
  exit 1
}

if [[ ! "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
  fail "expected a tag such as v1.5.2 or v1.6.0-rc.1"
fi

cd "$ROOT_DIR"

RELEASE_VERSION=${RELEASE_TAG#v}
BASE_TAG=${RELEASE_TAG%%-*}
CHART_VERSION=$(awk '$1 == "version:" { print $2; exit }' helm/kubeatlas/Chart.yaml)
APP_VERSION=$(awk '$1 == "appVersion:" { gsub(/\"/, "", $2); print $2; exit }' helm/kubeatlas/Chart.yaml)

[[ "$CHART_VERSION" == "$RELEASE_VERSION" ]] ||
  fail "Chart version $CHART_VERSION does not match $RELEASE_VERSION"
[[ "$APP_VERSION" == "$RELEASE_VERSION" ]] ||
  fail "Chart appVersion $APP_VERSION does not match $RELEASE_VERSION"

# shellcheck disable=SC1091
source images/postgres-age/image.env
[[ "$POSTGRES_AGE_REPOSITORY" =~ ^ghcr\.io/[a-z0-9._/-]+$ ]] ||
  fail "invalid POSTGRES_AGE_REPOSITORY"
[[ "$POSTGRES_AGE_TAG" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] ||
  fail "invalid POSTGRES_AGE_TAG"
POSTGRES_AGE_IMAGE="${POSTGRES_AGE_REPOSITORY}:${POSTGRES_AGE_TAG}"

CHART_POSTGRES_AGE_IMAGE=$(awk '
  /^persistence:/ { in_persistence = 1 }
  in_persistence && /^    image:/ { print $2; exit }
' helm/kubeatlas/values.yaml)
[[ "$CHART_POSTGRES_AGE_IMAGE" == "$POSTGRES_AGE_IMAGE" ]] ||
  fail "Helm uses $CHART_POSTGRES_AGE_IMAGE, expected $POSTGRES_AGE_IMAGE from image.env"

# If the image recipe changed since the previous release, reusing the same tag
# would make an existing installation resolve different bytes later. Derive the
# previous release from Git history, including when this check runs on a tag.
COMPARE_REF=HEAD
if git tag --points-at HEAD | grep -Fxq "$RELEASE_TAG"; then
  COMPARE_REF=HEAD^
fi
PREVIOUS_TAG=${PREVIOUS_RELEASE_TAG:-$(
  git describe --tags --abbrev=0 --match 'v[0-9]*' "$COMPARE_REF" 2>/dev/null || true
)}

if [[ -n "$PREVIOUS_TAG" ]] &&
   git cat-file -e "${PREVIOUS_TAG}:images/postgres-age/Dockerfile" 2>/dev/null; then
  PREVIOUS_IMAGE=$(git show "${PREVIOUS_TAG}:helm/kubeatlas/values.yaml" | awk '
    /^persistence:/ { in_persistence = 1 }
    in_persistence && /^    image:/ { print $2; exit }
  ')

  if ! git diff --quiet "$PREVIOUS_TAG" -- images/postgres-age/Dockerfile &&
     [[ "$PREVIOUS_IMAGE" == "$POSTGRES_AGE_IMAGE" ]]; then
    fail "PostgreSQL + AGE Dockerfile changed since $PREVIOUS_TAG but image tag did not"
  fi
fi

mkdir -p "$(dirname "$NOTES_OUT")"
make changelog-extract VERSION="$BASE_TAG" OUT="$NOTES_OUT"

printf 'release contract: %s matches Chart metadata and CHANGELOG\n' "$RELEASE_TAG"
printf 'release contract: PostgreSQL + AGE image is %s\n' "$POSTGRES_AGE_IMAGE"
if [[ -n "$PREVIOUS_TAG" ]]; then
  printf 'release contract: immutable recipe comparison used %s\n' "$PREVIOUS_TAG"
fi
