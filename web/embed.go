// Package web embeds the built WebUI so the production binary serves the API
// and the interface from a single process. There is no Node.js at runtime.
package web

import (
	"embed"
	"io/fs"
)

// dist is filled in by `npm run build` before `go build`. Only .gitkeep is
// checked in, so a fresh clone still compiles; Built() reports which case
// this binary is in.
//
//go:embed all:dist
var dist embed.FS

// FS returns the built asset tree rooted at the site root.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

// Built reports whether a real bundle is embedded.
func Built() bool {
	f, err := dist.Open("dist/index.html")
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Placeholder is served when the binary was built without the WebUI bundle.
// It is a page rather than an error so the operator sees exactly what to do.
const Placeholder = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Polyglot</title>
<style>
 body{font-family:ui-sans-serif,system-ui,sans-serif;margin:0;display:grid;
      place-items:center;min-height:100vh;background:#0b0b0c;color:#e7e7ea}
 div{max-width:34rem;padding:2rem;line-height:1.6}
 code{background:#1c1c1f;padding:2px 6px;border-radius:4px;font-size:.9em}
 a{color:#8ab4f8}
</style></head><body><div>
<h1>Polyglot</h1>
<p>The API is running, but this binary was built without the WebUI bundle.</p>
<p>Build it and rebuild:</p>
<p><code>npm --prefix web install &amp;&amp; npm --prefix web run build</code><br>
<code>go build ./cmd/polyglot</code></p>
<p>Or use <code>make build</code>, which does both. The Docker image always
includes the bundle.</p>
</div></body></html>`
