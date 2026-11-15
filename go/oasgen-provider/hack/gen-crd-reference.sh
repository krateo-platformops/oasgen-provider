#!/bin/sh
# Regenerate docs/restdefinition-crd-reference.md from the generated CRDs.
#
# CWD-INDEPENDENT on purpose. go:generate runs each directive with the cwd set to the directory of the
# FILE that holds it (apis/), not the module root, so the first version of this script resolved every
# path one level too high and exited 1 -- and because the caller only checked the regenerated file
# against itself, the failure first read as "idempotent". Paths are resolved from the script's own
# location instead, so it behaves the same from apis/, from the module root or from a CI step.
#
# WHY THIS IS A SCRIPT AND NOT A go:generate ONE-LINER: crdoc's --output OVERWRITES the whole file,
# and the reference carries eight lines of frontmatter (type/title/description/resource/tags/timestamp)
# that the docs site reads. The documented hand-run command therefore DESTROYED the frontmatter every
# time it was run, which is one reason nobody ran it -- and why the committed reference had fallen 641
# lines behind crds/, missing whole fields such as serverURL. The frontmatter is now a checked-in INPUT
# (restdefinition-crd-reference.header.md) that this script prepends, so regenerating is lossless.
#
# The timestamp in the header is the document's own date and deliberately does NOT track the
# regeneration: a timestamp that moved on every codegen run would put a spurious hunk in every diff and
# make the drift guard below useless for telling a real schema change from a no-op.
set -eu
HERE="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"   # go/oasgen-provider/hack
MOD="$(dirname -- "$HERE")"                              # go/oasgen-provider
ROOT="$(dirname -- "$(dirname -- "$MOD")")"              # the repo root
OUT="$ROOT/docs/restdefinition-crd-reference.md"
HDR="$ROOT/docs/restdefinition-crd-reference.header.md"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
# Pinned via the tools import in apis/generate.go, NOT @latest: an unpinned generator makes codegen
# non-reproducible, so the drift check below would fail for a reason unrelated to the schema.
cd "$MOD" && go run fybrik.io/crdoc --resources crds/ --output "$TMP"
cat "$HDR" "$TMP" > "$OUT"
