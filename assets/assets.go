// Package assets owns the front-end sources and their compilation. The Stylus
// and TypeScript sources are embedded into the binary at build time, then
// compiled to CSS/JS in-process at startup — no Node, no external toolchain,
// and a single self-contained deployable.
//
//	styles.styl --(go-styl)--> compressed CSS   served at /assets/app.css
//	app.ts      --(esbuild)--> minified JS      served at /assets/app.js
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

// Asset is a compiled front-end file together with the cache validator served
// alongside it. The tag is derived from the compiled output rather than the
// source, so it tracks exactly what the client receives — a compiler upgrade
// that changes the emitted bytes changes the tag, and a source edit that
// minifies to the same output does not.
type Asset struct {
	Body string
	ETag string
}

// etagFor returns a strong ETag for body: the leading 8 bytes of its
// SHA-256, hex-encoded and quoted as RFC 9110 requires. Truncating is fine
// here — the tag only has to tell one build's output apart from another's,
// and 64 bits makes an accidental collision between two versions of the same
// file implausible.
func etagFor(body string) string {
	sum := sha256.Sum256([]byte(body))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
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

// CSS returns the compiled, compressed stylesheet and its ETag.
func CSS() (Asset, error) { return compiledCSS() }

// JS returns the transpiled, minified client script and its ETag.
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
	return Asset{Body: css, ETag: etagFor(css)}, nil
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
	js := string(res.Code)
	return Asset{Body: js, ETag: etagFor(js)}, nil
}
