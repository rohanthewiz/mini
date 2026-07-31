package web

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/rweb"

	"mini/store"
)

// startTestServer spins up the full route table on a random port, backed by a
// throwaway bytdb file in a temp dir, and returns the server's base URL along
// with the store so callers (benchmarks especially) can seed data directly.
// testing.TB lets both tests and benchmarks share it; opts tune the engine
// (benchmarks pass bytdb.WithSyncNever() to make seeding cheap).
func startTestServer(t testing.TB, opts ...store.Option) (baseURL string, st *store.Store) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), opts...)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Tests keep per-request logging (it is only surfaced when a test fails,
	// where it is exactly the diagnostic you want); benchmarks turn it off,
	// since rweb.RequestInfo prints straight to stdout and would otherwise
	// interleave with — and bury — the ns/op lines.
	_, isBench := t.(*testing.B)

	ready := make(chan struct{}, 1)
	s := newServer(rweb.ServerOptions{
		Address:   ":0",
		ReadyChan: ready,
		Verbose:   !isBench,
	}, st)

	go func() {
		if err := s.Run(); err != nil {
			t.Logf("server.Run returned: %v", err)
		}
	}()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready in time")
	}

	return "http://" + s.GetListenAddr(), st
}

// get fetches a URL and returns the response and body, failing the test on
// transport errors.
func get(t testing.TB, url string) (*http.Response, string) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s: %v", url, err)
	}
	return resp, string(body)
}

func TestHealthEndpoint(t *testing.T) {
	base, _ := startTestServer(t)

	resp, body := get(t, base+"/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

func TestRootEndpoint(t *testing.T) {
	base, _ := startTestServer(t)

	// Each GET / records a visit, so the second view must show a count of 2 —
	// this exercises the whole loop: handler -> bytdb insert -> scan -> HTML.
	get(t, base+"/")
	resp, body := get(t, base+"/")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Attribute order within a tag is not guaranteed, so match with a regex.
	if !regexp.MustCompile(`id="live-visits"[^>]*>2<`).MatchString(body) {
		t.Fatalf("page does not show a visit count of 2; body:\n%s", body)
	}
	if !strings.Contains(body, "/assets/app.css") || !strings.Contains(body, "/assets/app.js") {
		t.Fatalf("page does not reference compiled assets; body:\n%s", body)
	}
}

func TestStatusEndpoint(t *testing.T) {
	base, _ := startTestServer(t)

	// One page view first, so the visits field has something to report.
	get(t, base+"/")

	resp, body := get(t, base+"/api/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// The cached path writes raw bytes, so it has to set this itself — where
	// rweb's WriteJSON used to do it.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if payload["response"] != "OK" {
		t.Fatalf(`payload["response"] = %v, want "OK"`, payload["response"])
	}
	// JSON numbers decode as float64.
	if payload["visits"] != float64(1) {
		t.Fatalf(`payload["visits"] = %v, want 1`, payload["visits"])
	}
}

// TestStatusCacheReusesBodyWithinTTL pins the point of the cache: a changed
// visit count is *not* reflected until the window passes. Uses an
// effectively-infinite TTL rather than a sleep, so the assertion cannot flake
// on a loaded machine.
func TestStatusCacheReusesBodyWithinTTL(t *testing.T) {
	c := newStatusCache(time.Hour)

	count := 1
	first, err := c.body(func() int { return count })
	if err != nil {
		t.Fatalf("first body: %v", err)
	}
	if !strings.Contains(string(first), `"visits":1`) {
		t.Fatalf("first body = %s, want visits 1", first)
	}

	count = 99
	second, err := c.body(func() int { return count })
	if err != nil {
		t.Fatalf("second body: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("body refreshed inside the TTL: %s -> %s", first, second)
	}
}

// TestStatusCacheRefreshesAfterTTL exercises the other branch. A zero TTL
// expires every entry the instant it is stored, so the refresh is forced
// deterministically — again, no sleeping.
func TestStatusCacheRefreshesAfterTTL(t *testing.T) {
	c := newStatusCache(0)

	count := 1
	if _, err := c.body(func() int { return count }); err != nil {
		t.Fatalf("first body: %v", err)
	}

	count = 99
	second, err := c.body(func() int { return count })
	if err != nil {
		t.Fatalf("second body: %v", err)
	}
	if !strings.Contains(string(second), `"visits":99`) {
		t.Fatalf("second body = %s, want visits 99", second)
	}
}

// getConditional issues a GET carrying If-None-Match, which the plain get
// helper cannot do.
func getConditional(t *testing.T, url, ifNoneMatch string) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request for %s: %v", url, err)
	}
	req.Header.Set("If-None-Match", ifNoneMatch)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s: %v", url, err)
	}
	return resp, string(body)
}

// TestAssetConditionalRequest walks the round trip a returning browser makes:
// fetch, remember the ETag, re-request with it, get a body-less 304. A stale
// tag must still get the full asset.
func TestAssetConditionalRequest(t *testing.T) {
	base, _ := startTestServer(t)

	for _, path := range []string{"/assets/app.css", "/assets/app.js"} {
		first, full := get(t, base+path)
		etag := first.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag on first response", path)
		}

		second, body := getConditional(t, base+path, etag)
		if second.StatusCode != http.StatusNotModified {
			t.Fatalf("%s: conditional status = %d, want 304", path, second.StatusCode)
		}
		if body != "" {
			t.Fatalf("%s: 304 carried a %d-byte body", path, len(body))
		}
		// The validator has to come back on the 304 too, or the client has
		// nothing to revalidate with next time.
		if got := second.Header.Get("ETag"); got != etag {
			t.Fatalf("%s: 304 ETag = %q, want %q", path, got, etag)
		}

		stale, body := getConditional(t, base+path, `"0000000000000000"`)
		if stale.StatusCode != http.StatusOK {
			t.Fatalf("%s: stale-tag status = %d, want 200", path, stale.StatusCode)
		}
		if body != full {
			t.Fatalf("%s: stale-tag body differs from the unconditional body", path)
		}
	}
}

// TestETagMatches covers the If-None-Match parsing rules that a real browser
// or proxy can exercise but the happy-path test above never will.
func TestETagMatches(t *testing.T) {
	const etag = `"abc123"`

	cases := []struct {
		name        string
		ifNoneMatch string
		want        bool
	}{
		{"absent", "", false},
		{"exact", `"abc123"`, true},
		{"wildcard", "*", true},
		{"weak prefix", `W/"abc123"`, true},
		{"list containing it", `"other", "abc123"`, true},
		{"list without it", `"other", "another"`, false},
		{"different tag", `"abc124"`, false},
		{"unquoted", "abc123", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := etagMatches(tc.ifNoneMatch, etag); got != tc.want {
				t.Fatalf("etagMatches(%q, %q) = %v, want %v",
					tc.ifNoneMatch, etag, got, tc.want)
			}
		})
	}
}

func TestAssetEndpoints(t *testing.T) {
	base, _ := startTestServer(t)

	resp, body := get(t, base+"/assets/app.css")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("css status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("css content-type = %q, want text/css", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != assetCacheControl {
		t.Fatalf("css cache-control = %q, want %q", cc, assetCacheControl)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Fatal("css response carries no ETag")
	}
	// Spot-check that Stylus actually compiled: the accent variable #0af
	// should appear as a resolved color value, not as a variable name.
	if !strings.Contains(body, "#0af") && !strings.Contains(body, "#00aaff") {
		t.Fatalf("compiled CSS missing accent color; got:\n%s", body)
	}

	resp, body = get(t, base+"/assets/app.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("js status = %d, want 200", resp.StatusCode)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Fatal("js response carries no ETag")
	}
	if !strings.Contains(body, "/api/status") {
		t.Fatalf("minified JS missing the status endpoint reference; got:\n%s", body)
	}
	// Minified output must not retain TypeScript syntax.
	if strings.Contains(body, "interface ") || strings.Contains(body, ": Promise<") {
		t.Fatalf("JS still contains TypeScript syntax; got:\n%s", body)
	}
}
