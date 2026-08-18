#!/usr/bin/env bash
#
# check-upstream-sync.sh — for a file in this fork, find its shared-history
# path on the very-srs/MANET upstream and list upstream commits since the
# fork point (2026-08-11) that touch it.
#
# Background: this fork went through two structural rewrites on 2026-08-11
# (node_tools/ -> scripts/{semantic dirs} -> rootfs/{etc,root,usr/...}),
# both git-tracked as renames. That means `git log --follow` on a current
# path bridges all the way back into shared upstream history for anything
# that's still a script/config (not rewritten into a Go service under
# src/ — a rewrite breaks rename detection since the content isn't similar
# enough for git to recognize).
#
# Usage:
#   ./check-upstream-sync.sh MANET/rootfs/usr/local/bin/radio-setup.sh
#
# Requires: `upstream` remote configured (https://github.com/very-srs/MANET.git)
# and fetched (`git fetch upstream`).
#
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

PATH_ARG="${1:?Usage: $0 <path-relative-to-repo-root>}"

if ! git rev-parse --verify upstream/main >/dev/null 2>&1; then
    echo "ERROR: no 'upstream/main' — run:" >&2
    echo "  git remote add upstream https://github.com/very-srs/MANET.git" >&2
    echo "  git fetch upstream" >&2
    exit 1
fi

if ! git cat-file -e "HEAD:$PATH_ARG" 2>/dev/null; then
    echo "ERROR: $PATH_ARG does not exist at HEAD." >&2
    exit 1
fi

echo "=== Fork history for $PATH_ARG (--follow bridges renames) ==="
git log --follow --oneline -- "$PATH_ARG"
echo ""

# Walk every rename hop in the file's fork history, newest-first, and use
# the first source path that actually exists on upstream/main. (The chain
# can go further back than the shared history — e.g. this fork's very own
# pre-node_tools/ layout — so we can't just take the oldest hop; we need
# the most recent one upstream still recognizes.)
mapfile -t RENAME_SRCS < <(git log --follow --name-status --format="" -- "$PATH_ARG" \
    | awk '/^R[0-9]*\t/ { print $2 }')

if [ "${#RENAME_SRCS[@]}" -eq 0 ]; then
    echo "No rename found in fork history for this file."
    echo "Either it was introduced fork-side after 2026-08-11 (no upstream"
    echo "lineage to check), or it's a rewrite (e.g. a Go service replacing"
    echo "a .py/.sh — git can't bridge that, content is too different)."
    exit 0
fi

OLD_PATH=""
for candidate in "${RENAME_SRCS[@]}"; do
    if git cat-file -e "upstream/main:$candidate" 2>/dev/null; then
        OLD_PATH="$candidate"
        break
    fi
done

if [ -z "$OLD_PATH" ]; then
    echo "None of this file's historical paths exist on upstream/main right now:"
    printf '  %s\n' "${RENAME_SRCS[@]}"
    echo "Upstream may have since renamed or removed it. Check manually:"
    echo "  git log upstream/main --oneline --all -- '<path>'"
    exit 0
fi

echo "Matching upstream path: $OLD_PATH"
echo ""

echo "=== Upstream (very-srs/MANET) commits on $OLD_PATH since the fork (2026-08-11) ==="
git log upstream/main --oneline --since=2026-08-11 -- "$OLD_PATH"
echo ""
echo "Full upstream history on that path:"
echo "  git log upstream/main --oneline -- '$OLD_PATH'"
echo "Inspect a commit:"
echo "  git show <sha> -- '$OLD_PATH'"
echo "Try applying it directly (works for content still in shell/config form):"
echo "  git cherry-pick <sha>"
