#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

BRANCH="${1:-main}"
UPSTREAM_BRANCH="${UPSTREAM_BRANCH:-upstream/main}"

cd "${REPO_ROOT}"

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "Worktree is dirty. Commit or stash changes before syncing upstream." >&2
  exit 1
fi

git fetch upstream --tags
git switch "${BRANCH}"
git rebase "${UPSTREAM_BRANCH}"

echo "Rebased ${BRANCH} onto ${UPSTREAM_BRANCH}."
echo "Next steps:"
echo "  1. Resolve conflicts if rebase stopped."
echo "  2. Run your validation/build."
echo "  3. Push with: git push origin ${BRANCH} --force-with-lease"
