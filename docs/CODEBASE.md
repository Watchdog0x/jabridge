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

`supportsValidatedPairingReads` in `pairing_policy.go` checks the dongle's USB ID.
`bluetooth_search.go` uses that policy for discovery. Headset models are not
hardcoded into this gate. See the [Link 390 test report](HARDWARE_TESTING.md#link-390-pairing-and-dongle-write-test)
for the tested dongle and headset.

### Pairing writes

`supportsExperimentalDongleWrites` keeps remembered-device writes separate
from discovery and fresh pairing. `paired_devices.go` uses it for connect,
disconnect and forget. Search-connect checks `pairingPolicy.SearchConnect`
before stopping discovery or opening the management interface. A daemon opt-in
can grant fresh search-connect for the 24c9 preview without granting remembered
or settings writes. The [hardware report](HARDWARE_TESTING.md#link-390-pairing-and-dongle-write-test)
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

`internal/history/history.go` includes fresh search-connect requests in the
bounded local history. Frequent search-status polling stays excluded. History
keeps the method, phase and classified error, not request parameters, addresses
or search tokens.

### Link 380c pairing preview

`pairing_policy.go` separates pairing-list/search access, fresh search-connect,
remembered-device writes and bound transport from the search wire format.
Link 380c 24c9 list, Classic discovery and pairing are off by default. Its
separate option enables all three and is captured only when the daemon starts. `devices.list.searchConnect` advertises
the effective permission to the terminal client; client environment variables
never authorize service writes. The service checks the policy before stopping
search or opening the pairing path. Remembered-device, settings and reset
gates stay separate and unchanged.

This preview uses the existing captured USB control binding with the legacy
Classic start/stop masks. `PairLegacyBound` translates discovery flag 1 into
Classic pairing namespace 0, sends one pair request after the session stops,
and reuses the bounded confirmation loop. Binding a device never changes its
wire masks. Acknowledgement alone is not success, writes are never replayed,
and confirmation stays on the captured dongle and selected address. The tested
24c7/24c8 path and Link 390 dual-mode flow retain their wire behavior.

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

`setting_metadata.go` carries model help, groups and restart information from
successful reads to IPC, the menu and CLI. Local help and read-only reasons
take priority. Controller parts never inherit the headset's model text.
`modelcatalog.DisplayText` removes terminal controls and limits descriptions;
duplicate metadata merges deterministically, keeping any restart warning.
The optional IPC `requiresRestart` field records catalog advice separately
from the existing `mayRestart` configuration-mode warning. Unknown restart
information is omitted; a fallback profile cannot promise no restart.
These descriptions never grant writes or change configuration-mode packets.

`equalizer.go` validates the five bands against the exact model's static
frequencies, Q and gain limits. Each row exposes half-dB choices, while encoding
preserves the master gain and every other band's freshly read raw fields.
`choice_payload.go` owns the shared codec contract and a cache scoped to one
settings snapshot, so five EQ rows need one device query. It never caches writes.
Unknown layouts show one generic read-only Equalizer row instead of guessing
band frequencies. Other structured controls retain their own unavailable row.

The equalizer editor in `tui_settings_editor.go` shows the actual current level,
starts near it even when it is between steps, and uses arrows for 1 dB or
Shift+arrows for 0.5 dB. It reserves separate rows for current value, step hints,
the scroll window and Save/Cancel, including at 40×20.
An invalid current value defaults to 0 dB, and Enter without moving an off-step
selection makes no change.

`settings_transaction.go` holds shared device access before taking the separate
settings-write lease. Only read-modify-write operations take this nonblocking
cross-process lock; scalar writes and normal reads keep their existing path.
The lease lasts through full readback and prevents overlapping Jabridge changes
from overwriting another band or preserved field. Firmware keeps its own lock.
New power-saving and structured writes also verify the bound firmware version
on their held transport, because a headset behind a dongle can update without
changing the dongle's USB attachment. No new check is added to old scalar writes.

`noise_cycle.go` validates the model's button-cycle members and encodes its
length-prefixed list. Separate boolean members do not establish valid mode
combinations. The default offers the full catalogued cycle and the current
read value; selecting the current value is a no-op before device access or
transaction locking. `setting_qualifications.go` narrows choices to reported
working combinations for an exact headset, firmware and dongle route. It does
not alias other models or firmware. Qualified choices bind the parent dongle's
attachment, PID, firmware and HID path. They are checked before the settings
lease, on the held transport just before the command, and after readback.
Dongle firmware comes from registry metadata; no extra dongle query is added.
An unobserved firmware change is not detected until that metadata or the USB
attachment changes. The codec also rejects unconfirmed new combinations before
a write and preserves the freshly read order.

`setting_capability.go` records model, exact-firmware, current-read and hardware
report evidence separately. Bool, choice and text resolvers reuse their existing
reads; they do not scan unknown commands or probe writes. IPC's optional
`support` object carries the same evidence into the menu and clients. A current
read proves that a control answered, not that every write works. Offline model
data leaves existing safe read probes read-only. Existing dongle write gates,
firmware guards, target identity and full readback remain required. There is no
known universal device command that lists every setting and allowed value.

The optional choice payload codec binds to an exact firmware profile before
editing, encodes from a fresh read, and verifies the full readback. Ordinary
single-byte choices retain their existing encoder and readback rules.

`voice_language.go` reads the complete 16-bit voice-language ID as a read-only
setting and links to the Firmware menu. It never sends a direct language write.
`tui_firmware_language.go` loads the current language and verified package
choices asynchronously, binds the file and USB attachment, and hands the
chosen language to the existing typed-confirmation installer. Cancel, an
unchanged selection and a stale target never prepare a hardware write.

`csr_language_plan.go` selects tunes content independently from main firmware;
both can use partition zero with different versions. `csr_language_update.go`
owns the new protocol-17 language-only policy: per-file version and LCID,
every batch acknowledgement, commit, serial/permission/configuration exit,
then fresh main-version, tunes-version, language and USB-generation checks.
The old protocol-16/17 transfer policy remains unchanged. The new language path
is limited to direct USB Evolve3 65 PID 2549/variant 01-A3 and an exact model
profile with matching main firmware. It has a separate default-off preview opt-in.

Recovery saves the source attachment and target language/pack before transfer
and before commit. Once a commit may have started, retry only reads and verifies;
it never erases, transfers or commits again. Normal firmware installation refuses
a pending language update. The sealed archive, official release checksum,
exclusive device lease and target binding remain required.

The power-saving boolean uses the vendor's off sentinel: every byte except
255 means on, while writes use 1 or 255. It requires an exact firmware profile
with both boolean choices before editing, and reuses the bound headset-part
transport and readback checks. Existing strict booleans keep their old decoder.

`modelcatalog.Property` retains composite member paths, fixed values and
editable members, plus special handling such as a required voice-language
pack. Existing flat values stay unchanged. Missing static/editable metadata
remains unknown so a structured control can refuse incomplete profiles.

`choice_settings.go` shares choice names between the menu, command line and
IPC. Auto-sleep labels match Jabra Plus's 15-minute to 8-hour choices; catalog
values still filter the supported raw bytes for each model. Bash completion
mirrors these names, with regression tests for both settings and IPC commands.

`settings.go` requires both write support and `validatedForDongle` for each
dongle setting. Only auto-pairing is writable on the tested Link 390; its
other settings remain read-only as described in the [test report](HARDWARE_TESTING.md#link-390-pairing-and-dongle-write-test).

### Implemented setting writes

`control_gate.go` and `control_gate_hidraw.go` coordinate Link 390 traffic per
physical USB port. Existing readers drain before ownership, and bound search
keeps its permit until close. Unknown-parent exchanges also participate, so
they cannot race the transition from idle to owned. Read-only passive HID
observers send no GNP and do not block ownership. See [the reader audit](READERS_AUDIT.md)
for the lock order, covered entry points and process-local boundary.

Link 390 `2e56` and `2e57`, variant `04-10`, firmware `3.2.0` also expose
preferred connection mode (`13/FB`) and computer audio (`0D/0F`). Their scalar
definitions require one-byte replies, a serial and attachment identity, and an
exact matching live model. Only the catalog intersection is offered. Other
dongles return before the extra catalog lookup; diagnostics apply the same
identity and model exclusion before issuing these reads.
These controls use the profiled held-handle firmware check, then one write
and verified readback. They do not grant pairing, reset or other dongle writes.
They have software coverage but no physical report.

`setting_write_policy.go` permits the implemented noise-cycle and equalizer
codecs by default. Power saving also needs no environment option. Unknown
structured codec types are refused before no-op handling, leases or transport;
client flags cannot grant permission. `exactSettingProfile` requires the actual
device firmware to be known as well as an exact catalog match. Model access,
allowed values, target identity, route binding and full readback remain required.

The bool and choice snapshot collectors accept a read function so tests can
exercise production filtering and provenance with simulated replies. Normal
callers use the existing device readers. Read-only rows explain the missing
model or implementation without adjustment advice. A noise cycle with no
different permitted choice does not offer an edit. Legacy scalar controls,
dongle qualification and separate firmware options keep their existing gates.

`pairingUIAllowed` uses the daemon's advertised 24c9 permission for client
visibility; it never authorizes backend IO. Both low-level pairing reads and
cached IPC lists use backend policy. Global pairing-mode commands also require
the existing remembered-write allowlist. Link 390 2e56 list/search have no
opt-in in this stable release; firmware testing is a separate permission.

### Factory reset

Link 390 PID 2e56 list and search are disabled in 1.5.0.
Its pairing, remembered-device and auto-pairing writes remain blocked until a
volunteer confirms the reads. The preview UI hides those actions. Other
Link 390 IDs and the Link 380 flow keep their existing gates.

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
