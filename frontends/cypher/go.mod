module github.com/panyam/jaala/frontends/cypher

go 1.26.4

require (
	github.com/panyam/jaala v0.1.70
	github.com/panyam/jaala/frontends/nl v0.0.0
)

// internal/guide, the fixture this module's TestEval shares with frontends/nl, is in no jaala tag yet:
// make bump-root after the next one drops this.
replace github.com/panyam/jaala => ../../

replace github.com/panyam/jaala/frontends/nl => ../nl
