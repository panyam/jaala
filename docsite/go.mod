module github.com/panyam/jaala/docsite

go 1.26.4

require github.com/panyam/s3gen v0.1.6

require (
	github.com/BurntSushi/toml v0.3.1 // indirect
	github.com/adrg/frontmatter v0.2.0 // indirect
	github.com/alecthomas/chroma v0.10.0 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/morrisxyang/xreflect v0.0.0-20231001053442-6df0df9858ba // indirect
	github.com/panyam/goutils v0.1.13 // indirect
	github.com/panyam/templar v0.1.0 // indirect
	github.com/radovskyb/watcher v1.0.7 // indirect
	github.com/yuin/goldmark v1.7.13 // indirect
	github.com/yuin/goldmark-highlighting v0.0.0-20220208100518-594be1970594 // indirect
	go.abhg.dev/goldmark/anchor v0.2.0 // indirect
	gopkg.in/yaml.v2 v2.3.0 // indirect
)

require (
	github.com/panyam/jaala v0.1.73
	github.com/panyam/jaala/frontends/cypher v0.0.0
	github.com/panyam/jaala/frontends/nl v0.1.73
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/panyam/jaala => ../

replace github.com/panyam/jaala/frontends/nl => ../frontends/nl

replace github.com/panyam/jaala/frontends/cypher => ../frontends/cypher
