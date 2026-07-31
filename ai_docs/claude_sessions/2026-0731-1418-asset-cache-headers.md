# Session: Cache-Control + ETag on the asset routes

- Session ID: `fd412bda-cf58-4e98-bcd5-674893aaaa8d`
- Date: 2026-07-31
- Branch: `main`
- Continues: `2026-0731-1333-status-microcache-readme.md` (same session, third part)

## What was done

Picked up the remaining caching item: `/assets/app.css` and `/assets/app.js`
now serve `Cache-Control` and a content-derived `ETag`, and answer conditional
requests with a body-less 304.

### 1. ETag computed at compile time (`assets` package)

`CSS()` / `JS()` changed from returning `(string, error)` to
`(Asset, error)`, where:

```go
type Asset struct {
    Body string
    ETag string
}
```

- The tag is the **leading 8 bytes of the output's SHA-256**, hex-encoded and
  quoted as RFC 9110 requires. Truncation is fine — the tag only has to tell
  one build's output apart from another's, and 64 bits makes an accidental
  collision implausible.
- Hashed from the **compiled** output, not the source, so it tracks exactly
  what the client receives: a compiler upgrade that changes emitted bytes
  changes the tag; a source edit that minifies to identical output does not.
- Computed inside `compileCSS` / `compileJS`, so it rides the existing
  `sync.OnceValues` — once per process, never per request.

Call-site churn was trivial: `main.go` already discarded the value
(`if _, err := assets.CSS(); err != nil`).

### 2. Cache-Control choice — `no-cache`, deliberately

The asset URLs are **stable across builds** (`/assets/app.css`, not
`/assets/app.<hash>.css`). A long `max-age` would therefore strand clients on
a stale bundle after a deploy with no way to bust it.

`no-cache` does **not** mean "do not store" — it means "store it, but
revalidate before reuse". That is the trade actually available here: the
client keeps the bytes, and revalidation costs a header-only 304 instead of
resending the asset.

Recorded in the code and README: fingerprinting the URLs
(`/assets/app.<hash>.css`) is what would unlock
`public, max-age=31536000, immutable`. That changes the rendered HTML
(`landingPage` emits the asset references), so it was left as a separate step
rather than widening the scope of this one.

### 3. `serveAsset` + `etagMatches` (`web/handlers.go`)

`serveAsset(ctx, asset, send)` sets both headers, then either returns 304 or
delegates the 200 to the rweb helper that owns the content type (`rweb.CSS` /
`rweb.JS` passed as `func(rweb.Context, string) error`) — so each asset route
stays a two-liner.

`etagMatches` implements the real If-None-Match grammar rather than a string
compare: comma-separated lists, `*`, and the `W/` weak prefix (conditional
GETs use the *weak* comparison function, so the prefix is stripped rather
than treated as part of the tag).

**API notes for rweb v0.1.26** (all confirmed this session):
- `ctx.Request().Header("If-None-Match")` reads a request header.
- `ctx.SetStatus(http.StatusNotModified)` sets the status; returning nil after
  it yields a body-less response.
- `ctx.Response().SetHeader(...)` with `consts.HeaderCacheControl`,
  `consts.HeaderETag`, `consts.HeaderIfNoneMatch`.
- `rweb.CSS` / `rweb.JS` set `text/css` / `text/javascript` and write the body.

## Verified on the wire

Ran the real server (`PORT=8123 go run .`) and curled it, not just the tests:

```
HTTP/1.1 200 OK                      HTTP/1.1 304 Not Modified
Content-Length: 723                  Content-Length: 0
Cache-Control: no-cache              Cache-Control: no-cache
ETag: "1c00c0ab01df9574"             ETag: "1c00c0ab01df9574"
Content-Type: text/css
```

The weak form (`If-None-Match: W/"83bac24c91595b3f"`) also returns 304, as
intended.

## Tests added

- `TestAssetConditionalRequest` (web) — the full browser round trip for both
  assets: fetch, re-request with the ETag, assert 304 + empty body + the
  validator echoed back (without it the client has nothing to revalidate with
  next time); a stale tag must still get 200 with the full body. Needed a new
  `getConditional` helper, since the existing `get` cannot set headers.
- `TestETagMatches` (web) — table over the parsing rules a real browser or
  proxy can exercise but the happy path never will: absent, exact, wildcard,
  weak prefix, list hit, list miss, different tag, unquoted.
- `TestETagsAreStableAndDistinct` (assets) — the tag does not move between
  calls, CSS and JS never collide, and the value is quoted.
- `TestAssetEndpoints` extended to assert both headers on CSS and the ETag on
  JS.

`go vet ./...`, `go test ./...`, `go test -race ./...` all green.
`BenchmarkHTTPAssetCSS` unchanged at 26.0µs (the added work is a header write
and an early-out string compare).

## Files changed

- `assets/assets.go` — `Asset` type, `etagFor`, `CSS`/`JS` return `Asset`
- `assets/assets_test.go` — updated to `.Body`, plus the ETag test
- `web/handlers.go` — `assetCacheControl`, `serveAsset`, `etagMatches`;
  `cssHandler`/`jsHandler` go through `serveAsset`
- `web/server_test.go` — `getConditional`, the two new tests, extended
  `TestAssetEndpoints`
- `README.md` — endpoint line and asset-pipeline section document the headers
  and the reasoning behind `no-cache`

## Possible next steps

- **Fingerprinted asset URLs** — `/assets/app.<hash>.css` emitted by
  `landingPage`, which would let the routes advertise
  `max-age=31536000, immutable` and drop revalidation entirely. The ETag hash
  computed this session is already the fingerprint needed.
- tsgo type-check of `app.ts` in CI (esbuild strips types but does not check
  them).
- Revisit `WithConcurrentWrites` only if independent writers ever contend
  (e.g. several unrelated tables writing in parallel).
