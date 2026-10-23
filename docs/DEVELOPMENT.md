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

## Install with Go

Use Go 1.27.1 or newer. Make sure Go's bin folder is in your PATH.

```sh
go install github.com/Watchdog0x/jabridge/cmd/jabridge@latest
jabridge setup
jabridge
```

## Package-managed builds

Let your package manager handle app updates and setup. From the source folder, run:

```sh
buildinfo=github.com/Watchdog0x/jabridge/internal/buildinfo
CGO_ENABLED=0 go build -buildvcs=false -o jabridge -ldflags "\
-X ${buildinfo}.SelfUpdate=disabled \
-X ${buildinfo}.Setup=disabled" ./cmd/jabridge
```

- `SelfUpdate=disabled` skips app update checks and prompts. The `update` command tells users to use their package manager.
- `Setup=disabled` turns off setup and its prompts. Jabridge does not install its binary copy, Bash completion or user service.

These options are separate. Only the exact value `enabled` allows a feature;
any other value turns it off. Official downloads enable both by default.
Device firmware is managed separately through the Firmware menu.

Use these files in your package:

| File | Purpose |
| --- | --- |
| `internal/assets/70-jabridge.rules` | USB device access |
| `internal/assets/jabridge.service` | Background user service |
| `internal/completion/jabridge.bash` | Bash completion |

Set the service's `ExecStart` to the full path of the packaged Jabridge followed
by `--daemon`. Start that service before opening the menu. The menu uses an
existing service with the same app version; it does not install or enable a unit.
The `service start` and `restart` commands use the packaged app without copying it.
With setup disabled, app update completion also leaves these files and the
service alone. The package controls its service setup and restarts.

If you previously ran `jabridge setup`, its unit at
`~/.config/systemd/user/jabridge.service` may override the packaged unit and keep
running the old app copy. Follow your package's migration steps to move that old
unit aside and use the packaged service.

For the source layout, see the [codebase guide](CODEBASE.md).
