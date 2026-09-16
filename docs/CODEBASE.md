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
       USB DFU / CSR / Sitel / UC Voice / PanaCast
                    |
           save recovery state
           transfer and verify
           check after restart
```

The menu remembers the selected USB attachment and firmware digest before
showing confirmation. The installer checks them again. An unplugged device
or changed file requires a new selection.

Use [the firmware guide](FIRMWARE.md) for individual protocols and model checks.
Wireless Engage adds a paired headset to that binding. The service supplies an
identity digest, `interactive_wireless_install.go` captures its USB parent,
and `sitel_ota_install.go` checks both devices after the service handoff. Its
wireless channel shares Sitel framing and CRC checks but has its own mode and
recovery sequence.

### Firmware files by job

| Job | Files in `internal/firmware` |
| --- | --- |
| Keep one immutable copy of the selected archive | `firmware_snapshot.go` |
| Match a model to its update route | `*_profiles.go`, `install_preflight.go` |
| Parse and check archive contents | `*_image.go`, `*_archive.go`, `sitel_images.go` |
| Drive an update and resume after interruption | `*_install.go`, `recovery.go` |
| Exchange device packets | `sitel_link.go`, `sitel_spi.go`, `*_transfer.go`, `*_protocol.go` |
| Open the selected Linux USB, HID or video interface | `*_linux.go`, `*_hidraw.go` |
| Stage PanaCast 50 files through UDisks | `camera_mass_storage.go` |

For Engage 75 and 75 SE, `engage75_archive.go` checks all nine components.
`sitel_dect_install.go` owns the base and headset lifecycle, while
`engage75_transfer.go` keeps the radio, settings and HEX transfers in order.
`sitel_bluecore_image.go` reads the radio images and ordered settings.
`sitel_spi.go` carries chip word reads and writes through the DECT base.
`sitel_internal_program.go` builds our standalone RAM flash program;
`sitel_internal_flash.go` uploads it, transfers sectors and checks readback.
`sitel_bccmd.go` discovers the running chip's settings mailbox, and
`sitel_psr.go` applies the selected chip's settings in file order.

The `*_sim_test.go` files model device responses and faults. They exercise
production host code, but do not execute the radio application or replace
physical hardware qualification. JabraCLI is an offline research reference;
Jabridge neither runs it nor requires its package.

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

The main UI handles input and service results separately from its 30 FPS paint
deadline in `tui_frame_clock.go`. It checks that deadline after every event so
busy input cannot starve drawing. Late timers do not cause catch-up bursts, and
animation uses elapsed time. An idle screen writes no repeated frames.
The input reader polls the terminal
without changing its shared file flags, so reading keys cannot make output
nonblocking and truncate the footer. Terminal write errors are returned.

`tui_search.go` polls search status in a worker. The UI loop applies results
only for the current search; leaving or restarting it cancels old work.
Start and Stop commands keep their order even when Back arrives during startup.
Cached native completion, timeout and failure states remain visible in the menu.
Terminal tests cover slow service replies, stale results, resizing, key hints
and output backpressure through a real pseudo-terminal.

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
