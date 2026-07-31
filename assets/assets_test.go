package assets

import (
	"strings"
	"testing"
)

// TestCSSCompiles guards the embedded Stylus source: a syntax error in
// styles.styl fails here at test time instead of at server startup.
func TestCSSCompiles(t *testing.T) {
	css, err := CSS()
	if err != nil {
		t.Fatalf("CSS(): %v", err)
	}
	if !strings.Contains(css.Body, "body") {
		t.Fatalf("compiled CSS missing body rule:\n%s", css.Body)
	}
	// Stylus variables must be resolved away, never emitted literally.
	if strings.Contains(css.Body, "accent") {
		t.Fatalf("compiled CSS leaked a Stylus variable name:\n%s", css.Body)
	}
}

// TestJSCompiles guards the embedded TypeScript source the same way.
func TestJSCompiles(t *testing.T) {
	js, err := JS()
	if err != nil {
		t.Fatalf("JS(): %v", err)
	}
	if strings.Contains(js.Body, "interface ") {
		t.Fatalf("esbuild left TypeScript syntax in output:\n%s", js.Body)
	}
	// Minification sanity: output should be substantially smaller than source.
	if len(js.Body) >= len(typeScriptSrc) {
		t.Fatalf("minified JS (%d bytes) is not smaller than source (%d bytes)",
			len(js.Body), len(typeScriptSrc))
	}
}

// TestETagsAreStableAndDistinct pins the two properties the cache headers
// depend on: the tag for a given build never moves (a client's stored copy
// stays valid), and the two assets never share a tag (a stale CSS can never
// validate against the JS).
func TestETagsAreStableAndDistinct(t *testing.T) {
	css, err := CSS()
	if err != nil {
		t.Fatalf("CSS(): %v", err)
	}
	js, err := JS()
	if err != nil {
		t.Fatalf("JS(): %v", err)
	}

	if css.ETag == "" || js.ETag == "" {
		t.Fatalf("empty ETag: css=%q js=%q", css.ETag, js.ETag)
	}
	// RFC 9110 requires the tag be quoted on the wire.
	if !strings.HasPrefix(css.ETag, `"`) || !strings.HasSuffix(css.ETag, `"`) {
		t.Fatalf("css ETag %q is not quoted", css.ETag)
	}
	if css.ETag == js.ETag {
		t.Fatalf("css and js share an ETag: %q", css.ETag)
	}

	again, err := CSS()
	if err != nil {
		t.Fatalf("CSS() again: %v", err)
	}
	if again.ETag != css.ETag {
		t.Fatalf("ETag changed between calls: %q -> %q", css.ETag, again.ETag)
	}
}
