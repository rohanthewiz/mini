# mini

A small, dependency-light Go web service: [rweb](https://github.com/rohanthewiz/rweb)
for HTTP, [element](https://github.com/rohanthewiz/element) for server-rendered
HTML, [bytdb](https://github.com/rohanthewiz/bytdb) for storage, and a
front-end build that runs in-process — **no Node, no cgo**.

## Run

```sh
go run .
```

The server listens on `:8000` by default.

| Env var   | Default   | Purpose                                        |
|-----------|-----------|------------------------------------------------|
| `PORT`    | `8000`    | Listen port                                     |
| `ENV`     | `dev`     | Environment name, reported by `/api/status`     |
| `DB_PATH` | `mini.db` | bytdb database file                             |

## Endpoints

- `GET /` — server-rendered landing page: records the visit, shows the running
  total and the five most recent visits
- `GET /api/status` — JSON `{response, env, visits}`; the client script polls
  it every 5s to keep the on-page counter live
- `GET /health` — liveness/readiness probe (returns `ok`)
- `GET /assets/app.css`, `GET /assets/app.js` — compiled front-end assets

## Front-end pipeline

Sources live in `assets/` and are embedded with `go:embed`, then compiled once
per process (`sync.OnceValues`) and served from memory:

- `styles.styl` → CSS via [go-styl](https://github.com/rohanthewiz/go-styl) —
  Stylus variables, mixins, and `darken`/`lighten` all resolve at compile time
- `app.ts` → minified ES2020 via [esbuild](https://github.com/evanw/esbuild)'s
  Go API — TypeScript stripping, minification, no toolchain to install

`main()` pre-warms both at startup, so a compile error fails the process
instead of surfacing as a 500 on the first request.

## Storage

One `visits` table in bytdb, an embedded relational engine over a single
ordered keyspace. Two things shape the `store` package:

- **Writes are batched, never on the request goroutine.** `RecordVisit`
  enqueues and returns; a single writer goroutine folds up to 256 queued
  visits into one transaction, so one fsync is amortized across the batch.
  `Flush()` is a FIFO barrier for read-your-writes.
- **The count is an atomic**, seeded by one scan at `Open`. Counting by
  scanning was ~160ns/row; this is O(1) at any table size.

`RecentVisits` is a reverse primary-key scan — the id is sequence-issued, so
"newest first" needs no index and no sort.

bytdb v0.7.0's opt-in concurrent-write (OCC) mode is deliberately **not**
used: this workload has exactly one writer, so there is no contention to
relieve, and OCC's non-transactional sequence draws would break the batch into
~9 commits instead of 1. The measurements and root cause are in the `store`
package doc.

## Performance

Measured on an Apple M3 (8 cores), full HTTP round-trips:

| Path                              | Cost                          |
|-----------------------------------|-------------------------------|
| `GET /health` (framework baseline)| ~25µs                         |
| `GET /api/status`                 | ~26µs, flat at 0–10k rows     |
| `GET /` (durable write)           | ~145µs                        |
| `GET /` under 8-way concurrency   | ~40µs/req (~25k req/s)        |
| `VisitCount()`                    | ~1.9ns, flat at any table size|
| Sustained durable visits          | ~13µs each (batched)          |

`/api/status` serves a pre-serialized body from a 1s micro-cache — every open
tab polls it, so N clients collapse into at most one marshal per second. Cache
hits are 30ns and allocation-free, against 470ns and 11 allocations for the
per-request marshal it replaced. The reported count may trail by up to a
second, which the client redraws every 5s anyway.

## Tests and benchmarks

```sh
go test ./...
go test -race ./...
go test ./... -bench . -run '^$'
```

Benchmarks turn off per-request logging (`startTestServer` sets
`Verbose: !isBench`), so `ns/op` lines are not buried in stdout. The first
iteration of the parallel benchmarks reads high — temp-dir warm-up; use
`-benchtime 3s` or discard it.

## Docker

```sh
docker build -t mini .
docker run --rm -p 8000:8000 mini
```

Alpine-based, non-root (uid 1001), static `CGO_ENABLED=0` binary.
