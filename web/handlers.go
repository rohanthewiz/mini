package web

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rohanthewiz/element"
	"github.com/rohanthewiz/logger"
	"github.com/rohanthewiz/rweb"
	"github.com/rohanthewiz/rweb/consts"
	"github.com/rohanthewiz/serr"

	"mini/assets"
	"mini/store"
)

// handlers groups the route handlers around their shared dependencies, so
// tests can stand up a handler set against a throwaway store instead of
// package-level state. status is a pointer because it carries a mutex and
// method values copy the receiver at route-registration time.
type handlers struct {
	st     *store.Store
	status *statusCache
}

// rootHandler records the visit, then server-renders the landing page showing
// the running total and the most recent visits. The record is an async
// enqueue (~ns) — the commit happens on the store's batch writer, so the
// page never waits on an fsync. A shed visit (queue full) is logged, not
// surfaced: analytics loss should not fail the page.
func (h handlers) rootHandler(ctx rweb.Context) error {
	if err := h.st.RecordVisit(ctx.Request().Path()); err != nil {
		logger.LogErr(err, "recording visit")
	}

	visitCount := h.st.VisitCount()

	recentVisits, err := h.st.RecentVisits(5)
	if err != nil {
		return serr.Wrap(err, "fetching recent visits")
	}

	return ctx.WriteHTML(landingPage(visitCount, recentVisits))
}

// statusHandler is the JSON status endpoint. The client script polls it to
// keep the on-page visit counter live — its shape is mirrored by the Status
// interface in assets/app.ts. The body is served from a short-lived cache
// (see statusCache), so the per-request work is a pointer load and a copy.
func (h handlers) statusHandler(ctx rweb.Context) error {
	body, err := h.status.body(h.st.VisitCount)
	if err != nil {
		return serr.Wrap(err, "building status response")
	}

	// ctx.Bytes is the raw-body writer, so the content type WriteJSON would
	// have set has to be set here instead.
	ctx.Response().SetHeader(consts.HeaderContentType, consts.MIMEJSON)
	return ctx.Bytes(body)
}

// statusCacheTTL is how long one serialized /api/status body is reused.
// Every open tab polls the endpoint every 5s, so this collapses N clients'
// polls into at most one marshal per second. The trade is that the reported
// count may trail reality by up to a second — immaterial for a number the
// client only redraws every 5s anyway.
const statusCacheTTL = time.Second

// statusPayload is the /api/status response body. A struct rather than a
// map[string]any: no map allocation, no key sorting inside encoding/json,
// and the wire shape is declared in exactly one place.
type statusPayload struct {
	Response string `json:"response"`
	Env      string `json:"env"`
	Visits   int    `json:"visits"`
}

// statusEntry is one immutable snapshot of the serialized response. Both
// fields are written before the entry is published to the atomic pointer and
// never mutated after, which is what lets readers work without a lock.
type statusEntry struct {
	body    []byte
	expires time.Time
}

// statusCache memoizes the serialized status body for ttl.
//
// Reads are lock-free — an atomic pointer load plus a deadline comparison.
// Refreshes take the mutex and re-check first, so a burst of pollers landing
// together on an expiry does one marshal between them rather than one each
// (the thundering-herd case this endpoint is most exposed to).
//
//	hit  ──> cur.Load ──> unexpired ──> return bytes
//	miss ──> mu.Lock ──> re-check ──> marshal ──> cur.Store ──> return bytes
type statusCache struct {
	cur atomic.Pointer[statusEntry]
	mu  sync.Mutex
	ttl time.Duration
}

// newStatusCache returns an empty cache with the given freshness window. A
// ttl of zero disables caching (every call re-marshals), which is what the
// tests use to exercise the refresh path without sleeping.
func newStatusCache(ttl time.Duration) *statusCache {
	return &statusCache{ttl: ttl}
}

// body returns the cached response bytes, refreshing them if the window has
// passed. The caller must treat the slice as read-only — it is shared by
// every request served from the same entry.
//
// visitCount is taken as a function, not a value, so the count is sampled at
// marshal time under the mutex: a goroutine that stalled on its way in can
// never publish an entry built from a stale reading.
func (c *statusCache) body(visitCount func() int) ([]byte, error) {
	if e := c.cur.Load(); e != nil && time.Now().Before(e.expires) {
		return e.body, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Someone may have refreshed while we waited for the lock.
	if e := c.cur.Load(); e != nil && time.Now().Before(e.expires) {
		return e.body, nil
	}

	body, err := json.Marshal(statusPayload{
		Response: "OK",
		Env:      envName(),
		Visits:   visitCount(), // atomic load — O(1) at any table size
	})
	if err != nil {
		return nil, serr.Wrap(err, "marshalling status payload")
	}

	c.cur.Store(&statusEntry{body: body, expires: time.Now().Add(c.ttl)})
	return body, nil
}

// cssHandler serves the stylesheet compiled from styles.styl. Compilation is
// cached after the first call (see assets package), so this is a string copy
// per request.
func cssHandler(ctx rweb.Context) error {
	body, err := assets.CSS()
	if err != nil {
		return serr.Wrap(err, "getting compiled CSS")
	}
	return rweb.CSS(ctx, body)
}

// jsHandler serves the minified client script transpiled from app.ts.
func jsHandler(ctx rweb.Context) error {
	body, err := assets.JS()
	if err != nil {
		return serr.Wrap(err, "getting compiled JS")
	}
	return rweb.JS(ctx, body)
}

// envName reports the deployment environment, defaulting to "dev" so the page
// always has something meaningful to display.
func envName() string {
	if env := os.Getenv("ENV"); env != "" {
		return env
	}
	return "dev"
}

// landingPage builds the full HTML document with element. The builder comes
// from the pool since this runs per-request on the hottest route.
func landingPage(visitCount int, recentVisits []store.Visit) string {
	b := element.AcquireBuilder()
	defer element.ReleaseBuilder(b)

	b.Html().R(
		b.Head().R(
			b.Meta("charset", "utf-8"),
			b.Meta("name", "viewport", "content", "width=device-width, initial-scale=1"),
			b.Title().T("mini"),
			b.Link("rel", "stylesheet", "href", "/assets/app.css"),
			// defer: execute after parse, so the script can touch the DOM
			// immediately without a DOMContentLoaded listener.
			b.Script("src", "/assets/app.js", "defer", "defer").R(),
		),
		b.Body().R(
			b.H1().T("mini"),
			b.DivClass("stats").R(
				b.DivClass("stat").R(
					// id targeted by app.ts, which live-updates the number
					// from /api/status.
					b.DivClass("num", "id", "live-visits").F("%d", visitCount),
					b.DivClass("label").T("visits"),
				),
				b.DivClass("stat").R(
					b.DivClass("num").T(envName()),
					b.DivClass("label").T("environment"),
				),
			),
			b.TableClass("visits").R(
				b.THead().R(
					b.Tr().R(
						b.Th().T("#"),
						b.Th().T("When (UTC)"),
						b.Th().T("Path"),
					),
				),
				b.TBody().R(
					element.ForEach(recentVisits, func(v store.Visit) {
						b.Tr().R(
							b.Td().F("%d", v.ID),
							b.Td().T(v.At.Format("2006-01-02 15:04:05")),
							b.Td().T(v.Path),
						)
					}),
				),
			),
		),
	)

	return b.String()
}
