module github.com/panyam/jaala/frontends/cypher

go 1.27.4

require (
	github.com/panyam/jaala v0.1.106
	github.com/panyam/jaala/frontends/nl v0.1.106
)

replace github.com/panyam/jaala => ../../

replace github.com/panyam/jaala/frontends/nl => ../../frontends/nl
