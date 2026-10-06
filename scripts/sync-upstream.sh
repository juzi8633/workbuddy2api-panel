#!/usr/bin/env bash
set -euo pipefail

# Only our fork is writable. The upstream is exclusively a fetch source.
fork_url='https://github.com/juzi8633/workbuddy2api-panel.git'
upstream_url='https://github.com/linguo2625469/workbuddy2api-panel.git'
if [[ "$(git remote get-url --push origin)" != "$fork_url" ]]; then
  echo 'Refusing sync: origin push URL must be our fork.' >&2
  exit 1
fi
if [[ "$(git branch --show-current)" != main ]] || [[ -n "$(git status --porcelain)" ]]; then
  echo 'Refusing sync: requires a clean main checkout.' >&2
  exit 1
fi
if git remote get-url upstream >/dev/null 2>&1; then
  git remote set-url upstream "$upstream_url"
else
  git remote add upstream "$upstream_url"
fi
git config remote.upstream.pushurl DISABLED
git config remote.pushDefault origin
git config branch.main.pushRemote origin
git fetch --no-tags upstream refs/heads/main:refs/remotes/upstream/main
if git merge-base --is-ancestor upstream/main HEAD; then
  echo 'Upstream main is already included.'
  exit 0
fi
# Never reset to upstream or force-push: merge preserves our own changes.
if ! git merge --no-ff --no-edit upstream/main; then
  git merge --abort
  echo 'Upstream merge conflicts: no changes were pushed. Resolve in our fork.' >&2
  exit 1
fi
go test ./...
go vet ./...
go build ./...
git diff --exit-code
# Explicit remote and branch; token is never sent to the upstream.
git push origin HEAD:refs/heads/main
