# Codebase guide

Jabridge has a terminal interface and a background service. Both use the same
Go packages to work with devices. No Jabra SDK or vendor helper is required.

[README](../README.md) covers the quick start. [Using Jabridge](USAGE.md)
has detailed commands and device limits. [Build and test](DEVELOPMENT.md)
includes package-managed builds.

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

`internal/buildinfo.SelfUpdate` is a build-time policy for package maintainers.
It defaults to `enabled` and can be set with Go's linker `-X` option. Only that
exact value permits app updates. `offerStartupAppUpdate` returns before creating
an update client when disabled; `runUpdate` returns a package-manager error
after help and argument parsing. `installAppUpdate` repeats the check before
output, service inspection, downloads or executable replacement. There is no
runtime override. This policy does not control device firmware or setup.

`internal/buildinfo.Setup` is a build-time policy for package maintainers. It
defaults to `enabled` and can be set with Go's linker `-X` option. Only that
exact value permits setup. `runSetup` returns a package-manager error after
help, before any helper or privileged step. `offerDeviceAccessSetupFor` reports
missing device access instead of offering setup. `installUserFiles` repeats the
check before it copies the executable, installs Bash completion or writes the
user service, so its other callers cannot bypass it. `prepareUserService` and
`installedUserExecutable` return the running executable for `service start` and
`restart` and for the no-systemd fallback. The terminal menu returns
`errPackagedServiceNotRunning` instead of installing and enabling a unit, and
`completeAppUpdate` has no copy to refresh. Starting the service is left to the
package's own unit. There is no runtime override, and device firmware is not
affected.

When setup is disabled, a matching service is used without the legacy
read-only-history check that normally refreshes a self-installed user unit.
The package owns that unit, and a history problem must not block the menu.
Setup and service help also explain package management when setup is disabled.

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

## Dongle pairing

### Reads and discovery

`supportsValidatedPairingReads` in `jabraApi.go` checks the dongle's USB ID.
`bluetooth_search.go` uses that policy for discovery. Headset models are not
hardcoded into this gate. See the [Link 390 test report](HARDWARE_TESTING.md#link-390-pairing-and-dongle-write-test)
for the tested dongle and headset.

### Pairing writes

`supportsExperimentalDongleWrites` keeps an independent USB ID list.
`paired_devices.go` uses it for connect, disconnect and forget. Search-connect
requires both read and write support before stopping discovery or opening the
management interface. The [hardware report](HARDWARE_TESTING.md#link-390-pairing-and-dongle-write-test)
lists the operations checked on Link 390.

For the candidate `2e57` search flow, `internal/btsearch` has a separate
dual-mode decoder and typed pairing transaction. Raw discovery flags are
translated into classic/LE namespaces; duplicate addresses with different types
remain separate choices. The service labels those choices for the client.
The legacy Link 380 decoder and command bytes are unchanged.

`bluetooth_search_bound.go` captures `firmware.ControlBinding` at search start.
That small wrapper reuses the firmware module's USB generation and opened-FD
parent/VID/PID checks. The binding follows neither a PID match nor a replacement
USB attachment. Available USB serials are checked in their own format.
An independent passive reader is opened before Stop/Pair. Attach/name events
accelerate a fresh connected-entry check; missing events fall back to reads no
faster than once a second. Each complete confirmation has a one-second deadline
inside the unchanged 30-second pairing budget. Writes are never replayed.

### Experimental Link 390 firmware

`csr_link390.go` recognizes the narrow monolithic package and owns the
protocol-7 transaction. Its stage uses the shared transfer primitive with an
explicit protocol-7 policy: 8-byte count/CRC header, 52-byte blocks, final-batch
acknowledgement, and version plus language. The existing extended 16/17 and
legacy partitioned gnV flows keep their wire behavior.

`csr_link390_linux.go` reads identity and protocol from the bound dongle.
Language may be unavailable for this policy, matching the vendor's fallback;
the candidate accepts only an unambiguous single-English image. Commit uses no
configuration-end command. Fresh runtime identity and requested firmware must
be read back, on either the same or a correctly rebound USB generation.

`csr_link390_install.go` integrates the existing sealed archive, official release
authorization, daemon pause, firmware lease and confirmation. It requires its
own process-start opt-in. Recovery is saved durably before transfer and before
commit. A `csr7-committing` record only permits read-only verification after
interruption. It never authorizes another erase or commit.

Archive inspection and installation share the profile validator. A successful
layout check does not claim that the device was tested or that installation is
enabled. See the candidate qualification limits in `HARDWARE_TESTING.md`.
`TEST_LINK390.md` contains the isolated review-service and firmware test steps.

### Setting validation

`choice_settings.go` shares choice names between the menu, command line and
IPC. Auto-sleep labels match Jabra Plus's 15-minute to 8-hour choices; catalog
values still filter the supported raw bytes for each model. Bash completion
mirrors these names, with regression tests for both settings and IPC commands.

`settings.go` requires both write support and `validatedForDongle` for each
dongle setting. Only auto-pairing is writable on the tested Link 390; its
other settings remain read-only as described in the [test report](HARDWARE_TESTING.md#link-390-pairing-and-dongle-write-test).

### Factory reset

`supportsValidatedFactoryReset` is separate and remains Link 380-only.
Both the menu and the IPC reset route reach this check before opening the
device. The [Link 390 reset report](HARDWARE_TESTING.md#link-390-pairing-and-dongle-write-test)
explains why that dongle is excluded.

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
repomap . --query 'firmware install recovery' \
  --mentioned-symbol PrepareInteractiveInstall
```

Then read the named functions and their callers. A map is a starting point;
tests and the actual code establish how the path behaves.

## Headset volume

`jabridge headset volume` follows `cmd/jabridge/headset_volume.go` through the
`device.volume` IPC method into `internal/headsetvolume`. It controls the
selected headset's USB audio gain. `jabridge sound volume` continues to use
PipeWire through `daemon/pipewire`.

The headset path checks the model, firmware, USB attachment and Audio 1
playback descriptors. It uses the Evolve2 30 SE firmware's endpoint request
route so Linux keeps its audio driver attached. It does not reset the device.
An internal probe checks the saved USB value before each write. Writes report USB request acceptance;
the firmware applies and saves changes asynchronously. History records these
requests separately from PipeWire changes.

`internal/headsetvolume/profiles.go` selects supported operations by model,
firmware and connection. The client asks the service for support through
`device.volume.info`, so the CLI does not maintain its own model allowlist.
`device.volume.get` is an explicit saved-level read. Discovery and debug never
run it automatically. A new transport needs its own verified protocol profile;
the existing USB request must not be redirected to a dongle audio interface.

## Silent USB playback recovery

`jabridge sound recover` calls `sound.recover` in the service.
`daemon/pipewire/audio_recovery.go` binds the output and microphone to the same
Evolve2 30 SE audio card, waits for its temporary capture to be active, sends
Suspend then Start to playback, and closes capture. Every stage checks the graph
identity and profile again. Existing calls/capture and other models are refused.

Recovery keeps capture active for at least two seconds before Suspend, waits
half a second before Start, and keeps capture for two seconds afterward. These
minimum delays come from the tester's successful script; graph state alone does
not establish that the USB device has settled. They remain cancellable.

`audio_recovery_process.go` runs `pw-cat` with a specific object serial and no
fallback, movement or reconnect to another microphone. Captured data is discarded.
The process has a time limit and a parent-death signal. Failure cleanup attempts
to restart only the original playback node before closing capture. Like the
existing `wpctl` controls, `pw-cli` resolves a numeric ID separately from the
snapshot check; this is not an atomic native PipeWire transaction.
The command includes the required empty command object (`{}`). `pw-cli` can
return exit status zero while printing an error, so stderr is also checked and
bounded. A successful return still does not prove audible recovery.

`cmd/jabridge/debug_usb_audio.go` reports numeric hub/controller IDs and matches
sticky-mixer warnings to the current USB port. These warnings may belong to an
earlier attachment on the same port. Raw kernel logs and USB serials are omitted.
The kernel mixer issue and hub-dependent silent playback remain distinct clues,
not diagnoses inferred from a kernel version or controller name alone.
