# Developer setup. The engine itself needs nothing but Go; Soufflé is only for the differential
# check in ./selfcheck.sh (#23), and is never a Go dependency.

SOUFFLE_DEB := x86_64-ubuntu-2404-souffle-2.5-Linux.deb
SOUFFLE_SHA512 := 6b86e554f6aa5abf8a8b55d8312ae37c0957c5bd6c9edeea89246db9406f645ec5e600b84fe6636b1c163da556f0da6c3d2dad46c1083413f2fcf4f95b9ac62c

.PHONY: setup souffle

setup: souffle

# Soufflé 2.5, the version CI's souffle job runs: Homebrew's bottle on macOS, the release .deb
# (as in .github/workflows/ci.yml) on Debian or Ubuntu x86_64.
souffle:
	@if command -v souffle >/dev/null 2>&1; then \
		echo "souffle already on PATH: $$(souffle --version 2>&1 | grep -i version | head -1)"; \
	elif [ "$$(uname -s)" = Darwin ]; then \
		brew install souffle; \
	elif command -v apt-get >/dev/null 2>&1 && [ "$$(uname -m)" = x86_64 ]; then \
		tmp=$$(mktemp -d) && \
		curl -fsSL -o "$$tmp/$(SOUFFLE_DEB)" "https://github.com/souffle-lang/souffle/releases/download/2.5/$(SOUFFLE_DEB)" && \
		echo "$(SOUFFLE_SHA512)  $$tmp/$(SOUFFLE_DEB)" | sha512sum -c - && \
		sudo apt-get install -y "$$tmp/$(SOUFFLE_DEB)"; \
		rm -rf "$$tmp"; \
	else \
		echo "no packaged Soufflé for $$(uname -s)/$$(uname -m): build 2.5 from source (souffle-lang.github.io/build)"; exit 1; \
	fi
