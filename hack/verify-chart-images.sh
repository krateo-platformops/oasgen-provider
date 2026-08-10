#!/usr/bin/env bash
# Assert every image referenced by the charts in this repo EXISTS in the registry, at the versions a
# release would actually publish.
#
# Why: chart 0.20.0 shipped rdc.image.tag: 0.19.0, a rest-dynamic-controller version never published in
# this org, so every generated controller ImagePullBackOff'd (#62). The cost was in WHERE the failure
# surfaced — at install time, inside a generated controller, several layers from the cause; the reported
# symptom was an unrelated SSO job hanging with its dependents stuck at Init:0/1.
#
# #63 removed the "forgot to bump the pin" half by deriving rdc.image.tag from .Chart.AppVersion. This
# closes the other half, which alignment cannot: oasgen-provider tagged while the matching
# rest-dynamic-controller image does not exist yet. Lockstep is a convention, and conventions lose to
# ordering mistakes.
#
# Every image is checked, not just the RDC one — the failure class belongs to any image a chart names,
# and it is no more code. That also makes the script reusable by charts whose companions are on
# INDEPENDENT version lines (core-provider pins chart-inspector 1.0.5 and cdc 1.3.15 against appVersion
# 2.13.0), where derivation is not available and this is the only protection.
#
#   APP_VERSION=0.20.0 ./hack/verify-chart-images.sh     # check against a specific release
#   ./hack/verify-chart-images.sh                        # resolve APP_VERSION as release-oci does
set -euo pipefail
cd "$(dirname "$0")/.."

CHART_VERSION="${CHART_VERSION:-${GITHUB_REF_NAME:-0.0.0}}"
# Mirrors release-oci.yaml: APP_VERSION is the latest semver tag of the app repo, which for this
# monorepo is the repo itself. Falls back to the local tags when gh is unavailable.
if [ -z "${APP_VERSION:-}" ]; then
  APP_VERSION="$(git tag --list --sort=-v:refname | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
fi
[ -n "$APP_VERSION" ] || { echo "ERROR: could not resolve APP_VERSION" >&2; exit 1; }
echo "resolved: CHART_VERSION=${CHART_VERSION}  APP_VERSION=${APP_VERSION}"

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
failed=0; checked=0

while IFS= read -r chart; do
  d="$(dirname "$chart")"
  # Skip vendored subcharts, matching release-oci's discovery rule.
  [ "$(basename "$(dirname "$d")")" = "charts" ] && continue
  cp -r "$d" "$work/$(basename "$d")"
  c="$work/$(basename "$d")"
  sed -i.bak "s/CHART_VERSION/${CHART_VERSION}/g; s/SOURCE_REF/${CHART_VERSION}/g; s/APP_VERSION/${APP_VERSION}/g" \
    "$c/Chart.yaml" && rm -f "$c/Chart.yaml.bak"

  # Images appear both as `image: "..."` in templates and inside the runtime-template ConfigMaps the
  # provider renders per RestDefinition, so grep the whole rendered output rather than parsing YAML.
  while IFS= read -r img; do
    [ -n "$img" ] || continue
    checked=$((checked+1))
    if docker manifest inspect "$img" >/dev/null 2>&1; then
      echo "  OK      $img"
    else
      echo "  MISSING $img   <- referenced by $(basename "$d")"
      failed=$((failed+1))
    fi
  done < <(helm template verify "$c" 2>/dev/null \
            | grep -oE 'image: *"?[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+"?' \
            | sed -E 's/^image: *"?//; s/"?$//' | sort -u)
done < <(find . -name Chart.yaml -not -path './.git/*' | sort)

echo "checked ${checked} image reference(s)"
if [ "$failed" -gt 0 ]; then
  echo "ERROR: ${failed} image(s) referenced by a chart do not exist in the registry." >&2
  echo "A release with these values would ImagePullBackOff on every install." >&2
  exit 1
fi
echo "OK: every chart-referenced image exists"
