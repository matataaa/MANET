#!/usr/bin/env bash
set -euo pipefail

# check-prebuilt-fresh.sh — fail when a committed prebuilt Go binary is older
# than its own source.
#
# The packaging scripts ship CGO binaries (src/<module>/bin/*) as committed,
# without rebuilding them. A source change committed without a rebuild then
# ships the old behaviour silently — mesh-voice went two weeks that way.
#
# For each binary src/<module>/bin/<file>, the module's sources are
# src/<module>/{*.go,go.mod,go.sum} (the *.go pathspec also covers vendor/).
# The binary is stale if any commit reachable from HEAD touches those sources
# but is not already contained in the commit that last changed the binary.
# That is the "binary commit older than newest source commit" rule, done on
# history rather than clock time, so rebases, merges and same-second commits
# are judged correctly. Uncommitted edits count too: edited sources with an
# untouched binary are stale; a locally rebuilt (modified) binary passes.
#
# Usage: check-prebuilt-fresh.sh <path/to/src/module/bin/binary>...

[ "$#" -ge 1 ] || { echo "usage: $0 <prebuilt-binary>..." >&2; exit 2; }

fail=0
for bin in "$@"; do
    if [ ! -f "$bin" ]; then
        echo "ERROR: prebuilt binary not found: $bin" >&2
        fail=1
        continue
    fi
    bin_dir="$(cd "$(dirname "$bin")" && pwd)"
    mod_dir="$(dirname "$bin_dir")"
    bin_abs="$bin_dir/$(basename "$bin")"

    if ! git -C "$mod_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        echo "WARNING: $mod_dir is not in a git work tree; cannot check $bin_abs is current" >&2
        continue
    fi
    srcs=("$mod_dir/*.go" "$mod_dir/go.mod" "$mod_dir/go.sum")

    # A binary rebuilt but not yet committed is taken as current.
    if [ -n "$(git -C "$mod_dir" status --porcelain -- "$bin_abs")" ]; then
        echo "OK (locally rebuilt, uncommitted): $bin_abs"
        continue
    fi

    if [ -n "$(git -C "$mod_dir" status --porcelain -- "${srcs[@]}")" ]; then
        echo "ERROR: $bin_abs is stale: uncommitted source changes in $mod_dir" >&2
        fail=1
        continue
    fi

    bin_commit="$(git -C "$mod_dir" log -1 --format=%H -- "$bin_abs")"
    if [ -z "$bin_commit" ]; then
        echo "ERROR: $bin_abs is not committed" >&2
        fail=1
        continue
    fi
    newer="$(git -C "$mod_dir" log --format='%h %ci %s' "$bin_commit..HEAD" -- "${srcs[@]}")"
    if [ -n "$newer" ]; then
        echo "ERROR: $bin_abs is stale (last rebuilt in $(git -C "$mod_dir" log -1 --format='%h %ci' "$bin_commit"))." >&2
        echo "       Source commits it does not contain:" >&2
        printf '%s\n' "$newer" | sed 's/^/         /' >&2
        echo "       Rebuild it (see $mod_dir/build.sh) and commit the binary." >&2
        fail=1
        continue
    fi
    echo "OK: $bin_abs"
done

exit "$fail"
