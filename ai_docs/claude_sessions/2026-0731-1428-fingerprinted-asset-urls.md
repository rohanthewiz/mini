# Session: fingerprinted asset URLs

- Session ID: `fd412bda-cf58-4e98-bcd5-674893aaaa8d`
- Date: 2026-07-31
- Branch: `main`
- Continues: `2026-0731-1418-asset-cache-headers.md` (same session, fourth part)

## What was done

Completed the caching work started in the previous part: the rendered page now
references fingerprinted asset URLs, which unlocks the year-long `immutable`
cache the bare paths could never safely advertise.

### 1. URL shape — hash in its own path segment

`/assets/<fingerprint>/app.css`, **not** `app.<hash>.css`. Two reasons:

- It routes as a plain rweb path parameter (`/assets/:v/app.css`). rweb splits
  patterns on `/`, so a mid-segment parameter was not an option anyway.
- The file name stays recognisable in dev tools / network panels.

The fingerprint is the same hash already computed for the ETag, so a URL match
and a header match can never disagree. `Asset` now carries all four derived
values, assembled once in `newAsset(body, name)`:

```go
type Asset struct {
    Body        string
    Fingerprint string // bare hex, appears in the URL
    ETag        string // same hash, quoted for the header
    URL         string // /assets/<fp>/app.css
}
```

### 2. Cache policy follows the URL, not the asset

Two constants replace the single `assetCacheControl`:

```go
assetRevalidateCC = "no-cache"
assetImmutableCC  = "public, max-age=31536000, immutable"
```

One comparison in `serveAsset` covers all three cases:

```go
cacheControl := assetRevalidateCC
if ctx.Request().Param(assetVersionParam) == asset.Fingerprint {
    cacheControl = assetImmutableCC
}
```

- unversioned route → param is `""` → revalidate
- stale fingerprint → mismatch → revalidate
- current fingerprint → match → immutable

### 3. Stale fingerprints serve the current asset, not 404

A page rendered seconds before a deploy asks for a fingerprint the new binary
does not have. Only one build exists in the binary (nothing on disk to fall
back to), so a 404 would break that page's styling. Serving the **current**
bytes under the revalidating policy means the mismatch self-corrects instead
of being cached for a year. Covered by
`TestStaleFingerprintServesCurrentAsset`.

### 4. Routing

Both pairs registered, hitting the same handlers:

```go
s.Get("/assets/app.css", cssHandler)
s.Get("/assets/app.js", jsHandler)
s.Get("/assets/:"+assetVersionParam+"/app.css", cssHandler)
s.Get("/assets/:"+assetVersionParam+"/app.js", jsHandler)
```

Segment counts differ (2 vs 3), so the patterns cannot collide.

### 5. URLs resolved once, at server construction

`landingPage` gained a `pageAssets{CSSURL, JSURL}` parameter rather than
reading the assets package itself, keeping the render a pure function of its
inputs. `assetURLs()` is called in `newServer` and stored on `handlers` — the
compiled output cannot change for the life of the process, so resolving per
render was pointless. On error it logs and falls back to the unversioned
paths (unreachable in production: `main()` pre-warms and exits on a compile
error, and if it were reached the asset routes would be failing too).

## Verified on the wire

Ran the real server and curled it, not only the tests:

```
page head:  href="/assets/1c00c0ab01df9574/app.css"
            src="/assets/83bac24c91595b3f/app.js"

/assets/1c00c0ab01df9574/app.css → 200, public, max-age=31536000, immutable
/assets/deadbeefdeadbeef/app.css → 200, no-cache   (stale fingerprint)
/assets/app.css                  → 200, no-cache   (bare path)
```

## Tests

- `TestFingerprintedAssetURLs` (web) — the URLs the page references serve
  under the immutable policy.
- `TestStaleFingerprintServesCurrentAsset` (web) — the deploy window: 200,
  current bytes, revalidating policy.
- `TestFingerprintedURLs` (assets) — URL embeds the fingerprint, ETag is the
  quoted fingerprint, fingerprint is 16 hex chars.
- `TestRootEndpoint` now asserts the page references the **fingerprinted**
  URLs (via `assetURLs()`), so a regression to bare paths fails loudly. This
  assertion had to change — it previously matched `/assets/app.css`, which is
  no longer in the rendered HTML.

`go vet ./...`, `go test ./...`, `go test -race ./...` all green.

## Benchmarks

- `BenchmarkHTTPAssetCSS`: 26.2µs — unchanged.
- `BenchmarkHTTPRootSyncNever/rows=0`: ~144µs, against ~142µs at the previous
  commit.

The `GET /` difference was **A/B'd rather than assumed**: `git stash`, measure
HEAD, `git stash pop`. HEAD ranged 141.0–145.1µs, this change 143.0–147.4µs —
overlapping ranges, ~1.5%, consistent with the ~34 extra bytes of HTML from
the longer URLs. Hoisting the asset lookup out of the request path (item 5
above) did not move it, which is how we know the lookup was not the cause.
Kept the hoist anyway: it is architecturally right and removes an error path
from the hot handler.

## Files changed

- `assets/assets.go` — `Fingerprint`/`URL` fields, `newAsset` replaces
  `etagFor`, package doc updated for the new URL shape
- `assets/assets_test.go` — `TestFingerprintedURLs`
- `web/handlers.go` — `assetRevalidateCC`/`assetImmutableCC`,
  `assetVersionParam`, URL-driven policy in `serveAsset`, `pageAssets`,
  `assetURLs`, `landingPage` takes the URLs, `handlers.assets`
- `web/server.go` — versioned routes; `assetURLs()` resolved once at
  construction with a logged fallback
- `web/server_test.go` — two new tests, `TestRootEndpoint` updated,
  `assetCacheControl` → `assetRevalidateCC`
- `README.md` — endpoints and asset-pipeline sections describe the
  fingerprinted URLs, the immutable policy, and the stale-fingerprint
  behaviour

## Possible next steps

- tsgo type-check of `app.ts` in CI (esbuild strips types but does not check
  them). This is the last item carried over from the original list.
- Revisit `WithConcurrentWrites` only if independent writers ever contend
  (e.g. several unrelated tables writing in parallel).
- Optional: drop the unversioned asset routes once nothing references them,
  leaving only immutable URLs.
