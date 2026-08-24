#!/usr/bin/env bash
# 将官方 upstream 合并进本 fork 的产品主干（默认 main）。
# 使用 merge（非 rebase），避免对已部署/已打 tag 的 main 做 force-push。
#
# 用法:
#   ./scripts/sync-upstream.sh
#   ./scripts/sync-upstream.sh main
#   ./scripts/sync-upstream.sh main upstream/main
#   UPSTREAM_REF=upstream/v0.1.170 ./scripts/sync-upstream.sh
#   MERGE_MSG='chore(upgrade): 升级 Sub2API 到 0.1.170' ./scripts/sync-upstream.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

BRANCH="${1:-main}"
UPSTREAM_REF="${UPSTREAM_REF:-${2:-${UPSTREAM_BRANCH:-upstream/main}}}"

cd "${REPO_ROOT}"

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "Worktree is dirty. Commit or stash changes before syncing upstream." >&2
  exit 1
fi

if ! git remote get-url upstream >/dev/null 2>&1; then
  echo "Remote 'upstream' is not configured." >&2
  echo "  git remote add upstream https://github.com/Wei-Shaw/sub2api.git" >&2
  exit 1
fi

git fetch upstream --tags

if ! git rev-parse --verify "${UPSTREAM_REF}" >/dev/null 2>&1; then
  echo "Upstream ref not found after fetch: ${UPSTREAM_REF}" >&2
  exit 1
fi

git switch "${BRANCH}"

if git merge-base --is-ancestor "${UPSTREAM_REF}" HEAD; then
  echo "Already up to date: ${BRANCH} already contains ${UPSTREAM_REF}."
  exit 0
fi

DEFAULT_MSG="chore(upgrade): merge ${UPSTREAM_REF} into ${BRANCH}"
MERGE_MSG="${MERGE_MSG:-${DEFAULT_MSG}}"

echo "Merging ${UPSTREAM_REF} into ${BRANCH}..."
set +e
git merge --no-ff "${UPSTREAM_REF}" -m "${MERGE_MSG}"
merge_status=$?
set -e

if [[ "${merge_status}" -ne 0 ]]; then
  echo ""
  echo "Merge stopped (conflicts or error)."
  echo "Next steps:"
  echo "  1. Resolve conflicts, then: git add <files> && git commit"
  echo "     (or abort with: git merge --abort)"
  echo "  2. Run validation/build."
  echo "  3. Push with: git push origin ${BRANCH}"
  echo "  4. Official upgrade release via GHCR: git tag vX.Y.Z-clarence.N && git push origin vX.Y.Z-clarence.N"
  exit "${merge_status}"
fi

echo ""
echo "Merged ${UPSTREAM_REF} into ${BRANCH}."
echo "Next steps:"
echo "  1. Run your validation/build."
echo "  2. Push with: git push origin ${BRANCH}"
echo "     (merge 后通常不需要 --force-with-lease)"
echo "  3. Official upgrade release via GHCR: git tag vX.Y.Z-clarence.N && git push origin vX.Y.Z-clarence.N"
echo "  See FORK_MAINTENANCE.md for the full workflow."
