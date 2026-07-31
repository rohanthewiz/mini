package web

import (
	"os"

	"github.com/rohanthewiz/element"
	"github.com/rohanthewiz/logger"
	"github.com/rohanthewiz/rweb"
	"github.com/rohanthewiz/serr"

	"mini/assets"
	"mini/store"
)

// handlers groups the route handlers around their one shared dependency (the
// datastore), so tests can stand up a handler set against a throwaway store
// instead of package-level state.
type handlers struct {
	st *store.Store
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
// interface in assets/app.ts.
func (h handlers) statusHandler(ctx rweb.Context) error {
	return ctx.WriteJSON(map[string]any{
		"response": "OK",
		"env":      envName(),
		"visits":   h.st.VisitCount(), // atomic load — O(1) at any table size
	})
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
