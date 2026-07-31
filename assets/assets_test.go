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
	if !strings.Contains(css, "body") {
		t.Fatalf("compiled CSS missing body rule:\n%s", css)
	}
	// Stylus variables must be resolved away, never emitted literally.
	if strings.Contains(css, "accent") {
		t.Fatalf("compiled CSS leaked a Stylus variable name:\n%s", css)
	}
}

// TestJSCompiles guards the embedded TypeScript source the same way.
func TestJSCompiles(t *testing.T) {
	js, err := JS()
	if err != nil {
		t.Fatalf("JS(): %v", err)
	}
	if strings.Contains(js, "interface ") {
		t.Fatalf("esbuild left TypeScript syntax in output:\n%s", js)
	}
	// Minification sanity: output should be substantially smaller than source.
	if len(js) >= len(typeScriptSrc) {
		t.Fatalf("minified JS (%d bytes) is not smaller than source (%d bytes)",
			len(js), len(typeScriptSrc))
	}
}
