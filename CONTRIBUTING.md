# Contributing

Thanks for helping with jaala. Issues and pull requests are welcome.

## AI-generated code

You may use AI tools to write your change. A person has to drive it, though. We don't accept pull
requests that an autonomous agent opened on its own.

A person driving the change means:

- A person picked the issue and decided the change was worth making.
- A person read every line of the diff and can explain it.
- A person ran the checks below and answers the review.

We close a pull request that an agent opened with no person behind it. When we can't tell, we may
ask about the change before we review it.

## Before you open a pull request

Say on the issue that you're working on it, so nobody builds it twice.

Run the checks CI runs:

```sh
gofmt -l .                          # must print nothing
go vet ./...
go test -race -count=1 ./...
GOOS=js GOARCH=wasm go build ./...
make -C docsite check
```

The engine (`ns`, `stdlib`, `datalog`) uses the standard library only, and it has to build for
wasm. Each directory under `frontends/` and `stores/` is a Go module of its own, and the root's
`./...` doesn't reach it, so run its tests from inside it.

A new or reworded `query:` error message goes on the docsite's error catalogue,
`docsite/content/reference/errors.md`. The "The error catalogue" section of `docsite/README.md`
says what an entry needs. `make -C docsite check` fails until the page lists the message.

To report a security issue, follow [SECURITY.md](SECURITY.md) instead of opening an issue.
