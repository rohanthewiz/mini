# Session: go-styl CSS, esbuild JS, bytdb store + performance work

- Session ID: `99ce12d6-e1f8-4f3d-8b85-7e46db7d354e`
- Date: 2026-07-30
- Branch: `main`

## What was done

### 1. Front-end pipeline — all in-process Go, no Node

- **CSS**: `assets/styles.styl` (Stylus: variables, `card()` mixin, `darken`/`lighten`) compiled by `github.com/rohanthewiz/go-styl` (`styl.Compile`). Variables resolve at compile time (e.g. `darken(#0af, 30%)` → `#0077b3`); compressed output is 723 bytes.
- **JS**: `assets/app.ts` (TypeScript) transpiled + minified by `github.com/evanw/esbuild/pkg/api` (`api.Transform`, `LoaderTS`, all three Minify flags, ES2020). ~1.2KB TS → 262 bytes JS.
  - Decision: esbuild over TypeScript-Go (tsgo) — tsgo type-checks but does not minify; esbuild embeds as a Go library and does minification + bundling + TS type-stripping. tsgo could complement in CI for real type checking.
- `assets/assets.go`: sources embedded via `go:embed`, compiled once per process via `sync.OnceValues`; `main()` pre-warms both to fail fast at startup. Served at `/assets/app.css` and `/assets/app.js` via `rweb.CSS`/`rweb.JS` helpers.

### 2. bytdb demo (`store/` package)

- `github.com/rohanthewiz/bytdb` embedded relational engine. Single `visits` table (`id` TInt PK, `at` TTimestamp, `path` TString); ids from named sequence `visit_id`.
- `GET /` records a visit and server-renders (element) count + last 5 visits; `GET /api/status` returns JSON `{response, env, visits}`; `app.ts` polls it every 5s to live-update `#live-visits`.
- Schema created only when `eng.Table("visits") == nil` (catalog persists in the db file). DB path from `DB_PATH` env, default `mini.db` (gitignored as `mini.db*`). Store closed via shutdown hook.

### 3. Performance work (benchmarked, then optimized)

Baseline findings (M3, 8 cores, full HTTP round-trips):
- Framework baseline ~27µs; durable insert 5.27ms (99.8% fsync — sync-never was 8.5µs); `VisitCount` full scan ~160ns/row (1.6ms @10k rows); `RecentVisits(5)` ~2µs at any size (reverse PK scan).

Optimizations implemented in `store/store.go`:
1. **O(1) count**: `atomic.Int64` seeded by one scan at `Open()`, incremented on accept, walked back on failed batch. `VisitCount()` now returns plain `int` (no error).
2. **Batched async writes**: `RecordVisit` enqueues (chan, depth 1024) and returns; single writer goroutine greedily drains up to 256 rows into one `Begin/Txn` commit (`Txn.NextSeq` inside the txn → one fsync per batch). Queue-full sheds with error (root handler logs, still serves). `Flush()` = FIFO barrier for read-your-writes; `Close()` drains queue before closing engine. Timestamps stamped at accept time.
- Caveat (documented): visits table lags the counter by up to one in-flight batch; `RecentVisits` sees committed rows only.

Results after:
- `VisitCount` @10k: 1.62ms → 1.8ns (flat); caller-side write: 5.27ms → 1.3µs; sustained durable throughput: ~190 → ~69k visits/s (14.4µs amortized); `/api/status` @10k: 1.71ms → 27.5µs (flat); `GET /` durable: 5.27ms → 161µs; parallel durable throughput: ~640 → ~24.5k req/s.
- bytdb showed ~3.4× group-commit scaling under 8-way concurrency even before batching.

### Tests / benchmarks

- `go test ./...` green, `-race` clean.
- `store/store_test.go`: count sync semantics, recent-order (uses `Flush`), multi-batch burst all lands, reopen reseeds counter, closed store rejects writes.
- `store/bench_test.go` + `web/bench_test.go`: run with `go test ./... -bench . -run '^$'`. Bench helpers use `store.Open(path, bytdb.WithSyncNever())` (`store.Option = btypedb.Option` passthrough added for this).
- `web/server_test.go`: `startTestServer` now returns `(baseURL, *store.Store)`, takes `testing.TB` + variadic `store.Option`. element attribute order is nondeterministic → HTML assertions use regex.

## Key API notes (for future reference)

- go-styl: `styl.Compile(src, styl.Options{})` — zero-value Options = compressed output; also `Globals`, `CustomProperties`, `Prune` (critical CSS), `FS` (embed.FS imports).
- bytdb: `Open(path, opts...)`, `CreateTable(name, []Column, pk...)`, `Insert`, `Get`, `Scan`, `ScanRangeRev(table, nil, nil, false)` = full reverse scan; `Begin(true)` → `Txn` (`NextSeq`, `Insert`, `Commit`); `TTimestamp` = int64 micros UTC; DDL not allowed inside txn; `bytdb.WithSyncNever()` disables fsync.
- rweb: `rweb.CSS(ctx, body)` / `rweb.JS(ctx, body)` set content types; `ServerOptions.ReadyChan`; `s.GetListenAddr()`.

## Possible next steps

- Micro-cache `/api/status` (~1s) since every tab polls it every 5s.
- `Cache-Control`/`ETag` on asset routes.
- tsgo type-check of `app.ts` in CI.
