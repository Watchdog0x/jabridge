# Build and test

Use the Go release in `.go-version` and golangci-lint 2.13.2. CI and `go.mod`
use the same supported Go release, including its current runtime defaults.

```bash
make check
make build
./dist/bin/jabridge --version
```

`make check` runs formatting, vet, race tests, static tests, Bash syntax and lint.
You can run one package with `go test ./cmd/jabridge` or all tests with
`go test ./...`. Tests that need separate real-device files skip when those
files are not supplied. A passing software test is not a hardware qualification.

The root is kept small. `cmd/jabridge` contains the app, `daemon` contains the
service and public IPC package, `internal` contains implementation packages and
installed assets, and `docs` contains guides and images.

Go unit tests stay beside the package they test, with names ending in
`_test.go`. This lets them test package internals without making those internals
public. Build outputs, firmware downloads and personal debug reports do not
belong in source control.

CI builds a review artifact. It does not publish a release. Release archives
must pass the self-updater's archive checks and be signed with the release key
before publication. Keep extra images outside executable archives so older
self-updaters can still read them.
