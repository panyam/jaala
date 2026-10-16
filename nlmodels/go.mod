module github.com/panyam/jaala/nlmodels

go 1.26.4

require github.com/panyam/jaala v0.1.64

// Until a jaala tag holds nl.Prompt.Grammar, nlmodels builds against the checkout it sits in. A
// consumer ignores replace, so nlmodels is tagged only once it requires that tag (CLAUDE.md, Releasing).
replace github.com/panyam/jaala => ../
