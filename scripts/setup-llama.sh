#!/usr/bin/env bash
# Installs llama.cpp's llama-server and fetches the 1 MB model its own CI tests with, for
# ./selfcheck.sh's check that a real decoder keeps a model to nl's grammar (#330). The model writes
# nonsense: the grammar is what's tested. It is ignored by git.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

MODEL=frontends/nlmodels/testdata/stories260K.gguf
SHA256=270cba1bd5109f42d03350f60406024560464db173c0e387d91f0426d3bd256d

if command -v llama-server >/dev/null 2>&1; then
    echo "llama-server already on PATH: $(llama-server --version 2>&1 | head -1)"
elif [ "$(uname -s)" = Darwin ]; then
    brew install llama.cpp
else
    echo "install llama.cpp's llama-server yourself (https://github.com/ggml-org/llama.cpp)"
fi

if [ ! -f "$MODEL" ]; then
    mkdir -p "$(dirname "$MODEL")"
    curl -fsSL -o "$MODEL" https://huggingface.co/ggml-org/models/resolve/main/tinyllamas/stories260K.gguf
    if ! echo "$SHA256  $MODEL" | shasum -a 256 -c -; then
        rm -f "$MODEL"
        exit 1
    fi
fi
