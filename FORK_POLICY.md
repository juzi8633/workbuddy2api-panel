# Our fork and upstream synchronization

Our authoritative repository is https://github.com/juzi8633/workbuddy2api-panel,
with `main` as the integration branch. Our wb17 modifications and later changes
belong here. The original repository is only an upstream source:
https://github.com/linguo2625469/workbuddy2api-panel.

Do not push branches, open pull requests, or submit issues containing our changes
to the upstream repository. All commits, pull requests and releases are made in
our fork. Keep the original Go module import path for compatibility; that path
does not select the destination for Git pushes.

Configure local checkouts:

```sh
git remote set-url origin https://github.com/juzi8633/workbuddy2api-panel.git
git remote add upstream https://github.com/linguo2625469/workbuddy2api-panel.git
# If upstream already exists, use remote set-url instead of remote add.
git config remote.upstream.pushurl DISABLED
git config remote.pushDefault origin
git config branch.main.pushRemote origin
```

The `Sync upstream into our fork` workflow runs daily at 03:23 China Standard Time
(19:23 UTC), and can also be run manually in our fork's Actions tab. GitHub may
delay scheduled runs. It fetches upstream `main`, merges it into our `main`, runs
Go tests, vet and build, then pushes only to our fork. It never resets our branch
to upstream and never force-pushes. A merge conflict or failed check stops the
update; resolve it in our fork and rerun. Review failed Actions runs to catch
conflicts promptly. GitHub can disable scheduled workflows after prolonged
inactivity; re-enable them in our fork's Actions tab when necessary.

`bash scripts/sync-upstream.sh` offers the same operation from a clean local
`main` checkout with Go and Node installed and our fork configured as `origin`.
It refuses any other origin push URL. The upstream push URL is disabled in the
script as well as in our working checkout.

The migration preserves the original local modification history and imports the
wb17 source snapshot, excluding patch backup files (`.orig` / `.rej`), binaries,
production accounts and runtime data. Existing wb batches are described in
`CHANGELOG.md`. Production deployment remains a separate action.
