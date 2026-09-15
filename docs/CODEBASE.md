# Codebase guide

Jabridge has a terminal interface and a background service. Both use the same
Go packages to work with devices. No Jabra SDK or vendor helper is required.

## Where to start

| Folder | Responsibility |
| --- | --- |
| `cmd/jabridge` | Commands, terminal menu, setup and service management |
| `daemon` | Device state, settings, buttons and background work |
| `daemon/ipc` | Local requests between the app and service |
| `daemon/pipewire` | Linux audio routing and volume |
| `internal/firmware` | Firmware downloads, validation, transfer and recovery |
| `internal/modelcatalog` | Jabra's published models, capabilities and releases |
| `internal/selfupdate` | Signed updates to the Jabridge application |
| `internal/history` | Private operation history used by debug reports |
| `internal/buildinfo` | Application name and default version |
| `internal/assets`, `internal/completion` | Setup files and Bash completion |

## Follow a device firmware update

```text
Firmware menu                    firmware install FILE.zip
      |                                    |
tui_firmware_update.go             install_preflight.go
      |                                    |
interactive_install.go                     |
      |                                    |
      +------> firmware.go <---------------+
                    |
              choose updater
          /         |          \
     USB DFU       CSR         Sitel
                    |
           save recovery state
           transfer and verify
           check after restart
```

The menu remembers the selected USB attachment and firmware digest before
showing confirmation. The installer checks them again. An unplugged device
or changed file requires a new selection.

Use [the firmware guide](FIRMWARE.md) for individual protocols and model checks.
Application updates are separate: `cmd/jabridge/app_update.go` calls
`internal/selfupdate`. They do not install headset firmware.

`main.go` calls `app_update_prompt.go` before starting an interactive menu or
command. The check has a short timeout, and the prompt reads only its own
answer line. An accepted offer and `jabridge update` both call
`installAppUpdate` in `commands.go`, then `completeAppUpdate` in
`app_update.go` refreshes completion and the installed service copy. Startup
offers restart the updated executable with the original arguments.

`tui_app_update.go` owns the update decision screen and its single-key input.
It stops its input reader and restores the terminal before handing control to
the installer or main menu. `tui_theme.go` holds the shared home/update palette.

## Build and check

```sh
make check
make build
dist/bin/jabridge --version
```

`make check` runs formatting, vet, race tests, static tests, completion checks
and golangci-lint. Use the Go version in `.go-version` and the linter version
in `.github/workflows/ci.yml`.

The default version lives in `internal/buildinfo/buildinfo.go`. The Makefile
and release workflow read it. `make build VERSION=...` overrides it for a
preview without changing the source default.

Tests with a `JABRIDGE_TEST_*` archive variable are optional local checks.
Their firmware files stay outside this repository and release archives.

## Find related code

If you have repomap installed, ask it for the parts related to your task:

```sh
repomap . --query 'firmware install recovery' --mentioned-symbol PrepareInteractiveInstall
```

Then read the named functions and their callers. A map is a starting point;
tests and the actual code establish how the path behaves.
