// Package assets owns the front-end sources and their compilation. The Stylus
// and TypeScript sources are embedded into the binary at build time, then
// compiled to CSS/JS in-process at startup — no Node, no external toolchain,
// and a single self-contained deployable.
//
//	styles.styl --(go-styl)--> compressed CSS   served at /assets/<fp>/app.css
//	app.ts      --(esbuild)--> minified JS      served at /assets/<fp>/app.js
//
// <fp> is a fingerprint of the compiled output, so each URL names one exact
// build and can be cached forever. The unversioned paths still serve the same
// bytes, but only under revalidation — see the web package.
package assets

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	styl "github.com/rohanthewiz/go-styl"
	"github.com/rohanthewiz/serr"
)

// Asset is a compiled front-end file together with everything needed to serve
// and reference it. All of it is derived from the compiled output rather than
// the source, so it tracks exactly what the client receives — a compiler
// upgrade that changes the emitted bytes changes the fingerprint, and a
// source edit that minifies to identical output does not.
//
// Fingerprint and ETag are the same hash in the two forms the HTTP layer
// needs: bare in a URL path, quoted in the header. Both are stored rather
// than derived per use, since neither can change once compiled.
type Asset struct {
	Body        string
	Fingerprint string // bare hex, appears in URL
	ETag        string // same hash, quoted for the header
	URL         string // fingerprinted path to reference from HTML
}

// newAsset assembles an Asset around a compiled body. name is the bare file
// name ("app.css") that the fingerprinted URL ends in.
//
// The fingerprint is the leading 8 bytes of the body's SHA-256, hex-encoded.
// Truncating is fine here — it only has to tell one build's output apart from
// another's, and 64 bits makes an accidental collision between two versions
// of the same file implausible.
func newAsset(body, name string) Asset {
	sum := sha256.Sum256([]byte(body))
	fingerprint := hex.EncodeToString(sum[:8])

	return Asset{
		Body:        body,
		Fingerprint: fingerprint,
		ETag:        `"` + fingerprint + `"`, // quoted, as RFC 9110 requires
		// The fingerprint gets its own path segment rather than being spliced
		// into the file name (app.<fp>.css): it routes as a plain parameter,
		// and the file name stays recognisable in dev tools.
		URL: "/assets/" + fingerprint + "/" + name,
	}
}

//go:embed styles.styl
var stylusSrc string

//go:embed app.ts
var typeScriptSrc string

// Each asset compiles exactly once per process. sync.OnceValues caches both
// the output and the error, so handlers can call CSS()/JS() freely — the
// embedded sources cannot change at runtime, so there is nothing to
// invalidate. main() calls these at startup to fail fast on a bad source.
var (
	compiledCSS = sync.OnceValues(compileCSS)
	compiledJS  = sync.OnceValues(compileJS)
)

// CSS returns the compiled, compressed stylesheet with its cache metadata.
func CSS() (Asset, error) { return compiledCSS() }

// JS returns the transpiled, minified client script with its cache metadata.
func JS() (Asset, error) { return compiledJS() }

func compileCSS() (Asset, error) {
	// Pretty:false (the zero value) emits compressed CSS, so minification
	// falls out of the compiler itself — no separate CSS minifier needed.
	css, err := styl.Compile(stylusSrc, styl.Options{})
	if err != nil {
		return Asset{}, serr.Wrap(err, "compiling styles.styl")
	}
	// Hashed once here, with the compile, rather than per request: the
	// output cannot change for the life of the process.
	return newAsset(css, "app.css"), nil
}

func compileJS() (Asset, error) {
	// esbuild runs as a library — the transform stays in-process. LoaderTS
	// strips type annotations (it does not type-check; that is an editor/CI
	// concern), and the three Minify flags together give full minification.
	res := api.Transform(typeScriptSrc, api.TransformOptions{
		Loader:            api.LoaderTS,
		Target:            api.ES2020,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
	})
	if len(res.Errors) > 0 {
		return Asset{}, serr.New("compiling app.ts: " + res.Errors[0].Text)
	}
	return newAsset(string(res.Code), "app.js"), nil
}
