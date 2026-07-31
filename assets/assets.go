// Package assets owns the front-end sources and their compilation. The Stylus
// and TypeScript sources are embedded into the binary at build time, then
// compiled to CSS/JS in-process at startup — no Node, no external toolchain,
// and a single self-contained deployable.
//
//	styles.styl --(go-styl)--> compressed CSS   served at /assets/app.css
//	app.ts      --(esbuild)--> minified JS      served at /assets/app.js
package assets

import (
	_ "embed"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	styl "github.com/rohanthewiz/go-styl"
	"github.com/rohanthewiz/serr"
)

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

// CSS returns the compiled, compressed stylesheet.
func CSS() (string, error) { return compiledCSS() }

// JS returns the transpiled, minified client script.
func JS() (string, error) { return compiledJS() }

func compileCSS() (string, error) {
	// Pretty:false (the zero value) emits compressed CSS, so minification
	// falls out of the compiler itself — no separate CSS minifier needed.
	css, err := styl.Compile(stylusSrc, styl.Options{})
	if err != nil {
		return "", serr.Wrap(err, "compiling styles.styl")
	}
	return css, nil
}

func compileJS() (string, error) {
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
		return "", serr.New("compiling app.ts: " + res.Errors[0].Text)
	}
	return string(res.Code), nil
}
