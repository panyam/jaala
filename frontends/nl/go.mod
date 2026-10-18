module github.com/panyam/jaala/frontends/nl

go 1.26.4

require github.com/panyam/jaala v0.1.70

// internal/guide, the fixture TestEval shares with frontends/cypher, is in no jaala tag yet: make
// bump-root after the next one drops this.
replace github.com/panyam/jaala => ../../
