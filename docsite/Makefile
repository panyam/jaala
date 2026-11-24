.PHONY: run build wasm wasm-test check test clean

# Serve the site at http://localhost:8085/jaala/. It builds once and has no file watcher, so restart
# it after an edit.
run: wasm
	JAALA_DOCS_PORT=:8085 go run .

# Build the site into ./dist. s3gen writes pages only, so static/ is copied in after; without it the
# built site has no CSS or scripts. .nojekyll stops GitHub Pages from dropping files that start
# with an underscore.
build: wasm
	go run . -build
	rm -rf dist/static
	cp -r static dist/static
	touch dist/.nojekyll

# jaala compiled for the browser, for the in-page editor, and the wasm_exec.js glue from the same Go
# toolchain, which it has to match. Both go in static/wasm, which git ignores; pages link them with a
# hash of their content (assetURL), so a browser never pairs an old glue file with a new module. The
# glue is read-only when the Go is a GOTOOLCHAIN download, which lives in the module cache, and the
# copy keeps that mode, so -f lets the next build replace it.
wasm:
	mkdir -p static/wasm
	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o static/wasm/jaala.wasm ./wasm
	cp -f "$$(go env GOROOT)/lib/wasm/wasm_exec.js" static/wasm/

# The demo package's tests compiled to wasm and run under Node, so the code the browser runs is
# tested as wasm, not only as Go. Needs node on the PATH. wasm_exec.js refuses a program whose
# arguments and environment pass about 12 KB, which a CI runner's environment does, so the tests run
# with only what go needs.
wasm-test:
	env -i PATH="$$PATH" HOME="$$HOME" GOPATH="$$(go env GOPATH)" GOCACHE="$$(go env GOCACHE)" \
		GOOS=js GOARCH=wasm go test -count=1 -exec="$$(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./demo

# The docsite's tests: templates parse, navigation is wired, includes resolve, and every internal
# link in the built site resolves (which needs the build first).
test: build wasm-test
	go test -count=1 ./...

# The mission's exercise (#104): build the site and run every check on it.
check: test

clean:
	rm -rf dist static/wasm
