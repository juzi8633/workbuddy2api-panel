# wb20 upstream integration and production inspection

## Scope

Integrate upstream `84614197edb08068cd9dabf471b0ebef2a27af0b` (2026-10-09) into our fork after deployed wb19 (`93025a9`). The upstream remote remains read-only. Production deployment is separate from source integration.

Included upstream changes:

- `7595a0a`: repair native tool-call markup in streaming and aggregated responses.
- `8391a05`: query expiry data on demand and reuse package data when opening the packages page.
- `8461419`: merge of upstream PR #123.

Conflicts were append-only test conflicts in `internal/server/handler_test.go` and `internal/upstream/sse_test.go`; both fork and upstream test groups are retained.

## Additional fixes

- Upstream issue #145: expose the expiry card and its Check button on initial rendering. Opening the accounts view still does not query the upstream. Initial text explains how to load expiry information.
- Upstream open PR #148: discard explicitly empty or whitespace-only function names from request history with their paired tool results. Preserve existing orphan cleanup and missing-field behavior. This change is scoped to history sanitation; it does not implement Codex subagent support.
- Advance the application version to `1.13.0-wb20` so the panel asset cache is invalidated for this batch.

## Production evidence (read-only)

Server: `118.145.237.3`, `workbuddy2api.service`, inspected at approximately 2026-10-11 00:00 UTC.

- Running wb19 binary SHA256: `9ac727dd073fe86a0899e137063b08d479f0444494e771a6cf3743c772593c95`, matching the prior deployment record.
- `/healthz`: healthy 5 / total 5; CN servable, global not servable.
- Completed request archive during the preceding 24 hours: 1,863 records, including 1,861 successes, one HTTP 400 `body_read_error`, and one HTTP 503 `no_healthy_account`.
- HTTP 400: `req-270113f1398c9394`, at 2026-10-10 20:20:15 +08:00, body-reading stage, 10,960 ms.
- HTTP 503: `req-94191cce5f395b79`, at 2026-10-11 07:57:15 +08:00, `cn:deepseek-v4.1-flash`, 6,408 ms. Journal entries around 07:57:17–20 contained `11133`.
- The archive contains no request body, so the observed 11133 does not establish that an empty tool name caused this production failure. The empty-name regression demonstrates the local sanitation defect independently.
- Latest two scheduled fork sync runs failed. The latest run completed checkout/tool setup and failed in the merge/verify/push step; local merging reproduced two test conflicts.

No production configuration, credentials or runtime state are included in this report.

## Validation

- Go 1.23.4: `go test ./...`, `go vet ./...` and `go build ./...` passed.
- `go test -race ./internal/upstream ./internal/server ./internal/panel` passed.
- `node --check internal/panel/app.js` passed.
- A focused Node assertion verified that the expiry card is visible, navigating to accounts does not call `loadExpiry`, and the Check button calls `loadExpiry(true)`.
- `git diff --check` passed. The upstream push URL remains `DISABLED`, and the default push remote is our `origin`.
- These checks do not demonstrate native Codex subagent support or prove the cause of the historical production 503.
