# Developer setup. The engine itself needs nothing but Go; Soufflé is only for the differential
# check in ./selfcheck.sh (#23), and llama.cpp for its check that a real decoder keeps a model to
# nl's grammar (#330). Neither is ever a Go dependency.

SOUFFLE_DEB := x86_64-ubuntu-2404-souffle-2.5-Linux.deb
SOUFFLE_SHA512 := 6b86e554f6aa5abf8a8b55d8312ae37c0957c5bd6c9edeea89246db9406f645ec5e600b84fe6636b1c163da556f0da6c3d2dad46c1083413f2fcf4f95b9ac62c

.PHONY: setup souffle llama

setup: souffle llama

# llama.cpp's server and the 1 MB model its own CI tests with, which writes nonsense: the grammar is
# what selfcheck tests, not the model. The model is ignored by git.
LLAMA_MODEL := frontends/nlmodels/testdata/stories260K.gguf
LLAMA_MODEL_SHA256 := 270cba1bd5109f42d03350f60406024560464db173c0e387d91f0426d3bd256d
llama:
	@if command -v llama-server >/dev/null 2>&1; then \
		echo "llama-server already on PATH: $$(llama-server --version 2>&1 | head -1)"; \
	elif [ "$$(uname -s)" = Darwin ]; then \
		brew install llama.cpp; \
	else \
		echo "install llama.cpp's llama-server yourself (https://github.com/ggml-org/llama.cpp)"; \
	fi
	@if [ ! -f $(LLAMA_MODEL) ]; then \
		mkdir -p $$(dirname $(LLAMA_MODEL)) && \
		curl -fsSL -o $(LLAMA_MODEL) https://huggingface.co/ggml-org/models/resolve/main/tinyllamas/stories260K.gguf && \
		echo "$(LLAMA_MODEL_SHA256)  $(LLAMA_MODEL)" | shasum -a 256 -c - || { rm -f $(LLAMA_MODEL); exit 1; }; \
	fi

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
