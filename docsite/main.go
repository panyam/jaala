// Command docsite builds and serves jaala's documentation site with s3gen. It is its own module, so
// s3gen and its dependencies never reach jaala's, which stays standard-library only and builds for
// wasm. `go run .` serves the site; `go run . -build` writes it to ./dist.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"html/template"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	s3 "github.com/panyam/s3gen"
)

var (
	addr  = flag.String("addr", defaultAddress(), "address to serve the site on")
	build = flag.Bool("build", false, "build the site once and quit, instead of serving it")
)

// projectRoot is the absolute path of the docsite directory, which includes resolve against.
var projectRoot string

func init() {
	var err error
	projectRoot, err = filepath.Abs(".")
	if err != nil {
		log.Fatalf("docsite: %v", err)
	}
}

// PathPrefix is the URL prefix every page and asset is served under: GitHub Pages serves the site at
// https://panyam.github.io/jaala/.
const PathPrefix = "/jaala"

// Site is the s3gen configuration for the site.
var Site = &s3.Site{
	OutputDir:       "./dist",
	ContentRoot:     "./content",
	PathPrefix:      PathPrefix,
	TemplateFolders: []string{"./templates"},
	StaticFolders:   []string{"/static/", "./static"},
	DefaultBaseTemplate: s3.BaseTemplate{
		Name:   "BasePage.html",
		Params: map[any]any{"BodyTemplateName": "Content"},
	},
	CommonFuncMap: map[string]any{
		"includeFile":     includeFile,
		"includeFileText": includeFileText,
		"siteVersion":     siteVersion,
		"assetURL":        assetURL,
		"dataset":         datasetHTML,
		"datasetURL":      datasetURL,
		"grammar":         grammar,
		"builtins":        builtinsHTML,
		"demo":            demoHTML,

		// Helpers newer s3gen has in its default func map and the pinned version lacks.
		"Contains":      strings.Contains,
		"HasPrefix":     strings.HasPrefix,
		"HasSuffix":     strings.HasSuffix,
		"BytesToString": func(b []byte) string { return string(b) },
		"HTML":          func(s string) template.HTML { return template.HTML(s) }, // #nosec G203 -- templates call it on this repo's own content
	},
}

func main() {
	flag.Parse()
	// Build once, then serve. There is no file watcher, so restart the server after an edit.
	Site.Rebuild(nil)
	exitOnDemoFailures()
	if !*build {
		Site.Serve(*addr)
	}
}

func defaultAddress() string {
	if a := os.Getenv("JAALA_DOCS_PORT"); a != "" {
		return a
	}
	return ":8080"
}

// includeFile returns a docsite-relative file as raw HTML, for a figure or a generated include.
func includeFile(relativePath string) template.HTML {
	data, ok := readDocsiteFile(relativePath)
	if !ok {
		return ""
	}
	return template.HTML(data) // #nosec G203 -- a figure or include from this repo, meant as HTML
}

// includeFileText returns a docsite-relative file as escaped text, for a source snippet.
func includeFileText(relativePath string) string {
	data, ok := readDocsiteFile(relativePath)
	if !ok {
		return ""
	}
	return string(data)
}

func readDocsiteFile(relativePath string) ([]byte, bool) {
	absPath, ok := safeJoin(relativePath)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(absPath) // #nosec G304 -- safeJoin keeps the path inside the docsite
	if err != nil {
		log.Printf("include: %v", err)
		return nil, false
	}
	return data, true
}

// safeJoin resolves a docsite-relative path and refuses one that is absolute or escapes the docsite.
func safeJoin(relativePath string) (string, bool) {
	cleanPath := filepath.Clean(relativePath)
	if filepath.IsAbs(cleanPath) || strings.HasPrefix(cleanPath, "..") {
		log.Printf("include: rejected path %q (absolute or escaping)", relativePath)
		return "", false
	}
	absPath, err := filepath.Abs(filepath.Join(projectRoot, cleanPath))
	if err != nil || !strings.HasPrefix(absPath, projectRoot) {
		log.Printf("include: rejected path %q (escapes the docsite)", relativePath)
		return "", false
	}
	return absPath, true
}

// siteVersion names the commit the site was built from, with the nearest tag, since the site
// documents main rather than a release: "v0.1.21-3-gb98b2d1". JAALA_DOCS_VERSION overrides it for a
// build outside a git checkout.
func siteVersion() string {
	if v := os.Getenv("JAALA_DOCS_VERSION"); v != "" {
		return v
	}
	out, err := exec.Command("git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "an unknown commit"
	}
	return strings.TrimSpace(string(out))
}

// assetURL is a static file's URL with a hash of its content, "/jaala/static/wasm/jaala.wasm?v=…", so
// a browser holding an older copy fetches the new one. The wasm module and wasm_exec.js have to come
// from the same Go toolchain, and Pages caches for ten minutes, so a stale glue file next to a new
// module would fail in ways that are hard to read. A file that isn't built gets no version.
func assetURL(relativePath string) string {
	url := PathPrefix + "/" + relativePath
	data, ok := readDocsiteFile(relativePath)
	if !ok {
		return url
	}
	sum := sha256.Sum256(data)
	return url + "?v=" + hex.EncodeToString(sum[:6])
}
