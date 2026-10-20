module github.com/panyam/jaala/stores/bolt

go 1.26.4

require (
	github.com/neo4j/neo4j-go-driver/v5 v5.28.5
	github.com/panyam/jaala v0.1.85
	github.com/panyam/jaala/frontends/cypher v0.1.85
)

require github.com/panyam/jaala/frontends/nl v0.1.85 // indirect

replace github.com/panyam/jaala => ../../

replace github.com/panyam/jaala/frontends/nl => ../../frontends/nl

replace github.com/panyam/jaala/frontends/cypher => ../../frontends/cypher
