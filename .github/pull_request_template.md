## What and why

<!-- What changes, and the problem it solves. Link the issue: Fixes #123 -->

## Before and after

<!-- For changes to output: the command and what it prints, before and after.
     For performance: numbers from bench_test.go. For import scanning or
     resolution: tools/accuracy before and after. Delete if not relevant. -->

## Checklist

- [ ] A test that fails without this change
- [ ] `gofmt -l .` prints nothing, `go vet ./...` and `go test -race ./...` pass
- [ ] `--help`, the man page (`completion.go`) and the README updated, if a flag or command changed
