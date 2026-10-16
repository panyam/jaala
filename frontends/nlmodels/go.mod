module github.com/panyam/jaala/frontends/nlmodels

go 1.26.4

require github.com/panyam/jaala v0.1.65

// Until a jaala tag holds frontends/nl, the clients build against the checkout they sit in. A
// consumer ignores replace, so this module is tagged (frontends/nlmodels/v0.1.N) only once it
// requires that tag (CLAUDE.md, Releasing).
replace github.com/panyam/jaala => ../../
