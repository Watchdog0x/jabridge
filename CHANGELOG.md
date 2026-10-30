# Changelog

## Unreleased

Add a Link 380c preview for USB ID 0b0e:24c9 (#58), which was recognized but
could not use Find headset. Reads and Classic search are enabled. Fresh pairing
requires its own daemon-start opt-in and stays tied to the original USB device.
Remembered-device changes, dongle settings writes and reset stay blocked.
This variant still needs a physical test; 24ca and 24e9 are unchanged.

Prepare 1.5.0 preview builds with read-only pairing-list and classic/LE search
for Link 390 USB ID 0b0e:2e56. Pairing writes and reset remain blocked while
the read-only preview is tested. The menu shows only available actions.

Add five equalizer band controls for matching headset profiles. The menu,
commands and Bash completion support half-dB values within the headset's limits.
The menu shows the current level and supports 1 dB arrow steps or 0.5 dB with
Shift. Changing one band preserves the other bands and checks the full readback.
Unsupported layouts stay read-only. Overlapping changes to structured or masked
settings are blocked while another change is in progress.

Add a Noise control button cycle setting for supported headset profiles.
Use model data and the current device reply rather than guessing every mode
combination. The tested Evolve3 65 firmware 1.0.1 through Link 390 PID 2e56
firmware 3.2.0 offers the three cycles reported to work. Other setups offer the
full catalogued cycle and the current value. Unconfirmed new combinations are
blocked before writing; selecting the current value makes no change. Reopen
Settings if the dongle changes. Changes
keep the existing mode order and are checked by reading the setting back.
Bash suggests the full cycle; commands validate choices for the selected device.

Settings now carry separate evidence for model data, current reads and hardware
test reports through the menu and IPC. Discovery uses existing safe reads and
adds no background polling or guessed device commands.

Add a read-only Voice prompt language setting and a USB voice-language update
path for the matching Evolve3 65 profile. The Firmware menu offers the languages
in the verified official package and asks for confirmation before updating.
Only the selected voice pack is transferred; the main firmware stays unchanged.
Interrupted commits keep a recovery record and can only be checked by reading
the headset. This path is experimental and needs an explicit preview opt-in.

Add a Power saving switch when the exact headset profile supports it, with
device readback after a change.

Keep the model's individual setting limits and language-pack requirements
when reading the available controls.

Fix doubled auto-sleep times in the menu and command line (#56). The choices
now run from 15 minutes to 8 hours, matching Jabra Plus. Bash completion uses
the same names. For example, `auto-sleep 30-min` now selects raw value 6 for
30 minutes; it previously selected raw value 3 for 15 minutes. The device
commands and catalog values are unchanged.

Prepare a Link 390 pairing fix for USB ID 0b0e:2e57. Search includes classic
Bluetooth and Bluetooth LE, keeps both choices visible, stops both searches
before pairing, and uses the connection type the user selected. Connection
checks stay bound to the original USB device. The Link 380 flow is unchanged.
A tester confirmed search and pairing without a USB replug after a reset with
Jabra Direct, on Link 390 2e57 firmware 1.3.0 and Evolve2 55 firmware 1.2.13.
The first empty search is unexplained. Jabridge's own reset stays disabled.

Add an experimental installer for the Link 390 single-file firmware format
reported in #54. It keeps the official checksum, device and protocol checks,
and stays off by default behind its own process-start opt-in. An interrupted
commit is checked by reading the installed version; it is never repeated
automatically. The real 3.2.0 package passed software simulation, including
transfer faults and process interruption. A tester reported a successful
3.2.0 installation and readback on Link 390 2e56; physical recovery remains
untested. File inspection now clearly distinguishes layout checks from
installation support. Link 390 factory reset remains disabled.

## 1.4.0 — 2026-10-25

Allow the Link 390 dongle with USB ID 0b0e:2e57 to use the pairing-list read,
headset search and search connect, remembered-device connect, disconnect and
forget, and the auto-pairing setting that were limited to two Link 380 USB IDs.
A Link 390 on firmware 1.3.0 was tested with an Evolve2 55: pairing-list reads,
search and connect, disconnect and reconnect, forget and re-pair, and an
auto-pairing write with read-back all succeeded. Factory reset completed and
re-enumerated the dongle, but the dongle needed a physical reconnect before it
would pair again, so factory reset keeps its Link 380-only allowlist. The
Bluetooth radio and softphone integration settings stay read-only on Link 390
until their writes are tested. The dongle pairing-mode command returned
success without pairing on this dongle. Other Link 390 USB IDs are added when
they are reported.

## 1.3.0 — 2026-10-23

Add a build-time option for distribution packagers to disable application
self-updates. Disabled builds skip startup checks and prompts, and the update
command directs users to their package manager. Official builds keep app
updates enabled by default. Device firmware updates are separate.

Add a build-time option for distribution packagers to disable setup. Disabled
builds refuse the setup command, stop offering it when device access is missing,
and no longer copy the executable, Bash completion or a user service into the
home folder from setup, the terminal menu or the service commands. They run the
packaged executable and leave device access rules and the service to the
package. Official builds keep setup enabled by default.

Package-managed menus keep using a matching service when its history folder is
read-only, without trying to replace the package's service unit.
Setup and service help direct packaged builds to their package manager.

## 1.2.1 — 2026-10-22

Fix USB DFU updates stopping while Linux is still granting access to the
restarted device. Retry only temporary node-open failures on the original USB
port, and use the device's advertised detach timeout.

Wait for pending download or finalization work before recovery. Retry final
status timeouts within the existing deadline without sending firmware blocks
again. A matching installed version is still required after restart.

Keep the full firmware error and recovery instructions visible in the terminal
after a menu-launched installation fails and the service has resumed.

The contributor in PR #50 reports a complete Evolve 75 direct-USB update from
2.32.0 to 2.38.0 on runtime ID 2465. The earlier Link 370 update on 1.2.0 needed
manual completion. Unattended patched Link 370 updates, other Evolve 75 variants and
recovery after power loss remain unqualified. The separate Evolve2 40 Sitel
preparation timeout remains unresolved.

## 1.2.0 — 2026-10-03

Add Off, ANC and HearThrough controls for Evolve2 85 using its `ancMode`
catalog property and confirmed command values. The contributor tested all three
modes through Link 380. Direct USB remains untested; the mixed HearThrough mode
and HearThrough level are not offered.

Debug saves a new numbered report when an older file exists and prints the
exact filename. Reports now retain firmware interface failure reasons and
include USB hub, controller and relevant kernel audio details.

Improve Sitel firmware reboot handling, recovery metadata and update-interface
selection. Evolve2 40 now opens the reported interface, but its latest hardware
test still times out during preparation before firmware transfer. A complete
firmware update remains unconfirmed.

Add experimental `jabridge sound recover` for Evolve2 30 SE over direct USB.
It briefly opens the headset microphone, discards capture and restarts playback.
It refuses calls, other recordings and changed device targets. PipeWire commands
include the required arguments, detect reported errors and wait between steps.
Audible recovery has not been confirmed; the separate kernel mixer problem is
not fixed by this command.

## 1.1.0 — 2026-09-17

Fix missing controls and incomplete terminal screens caused by the keyboard
reader changing shared terminal output flags. Search polling and device
selection now run outside the UI loop, keeping navigation and resizing
responsive while the service is busy.

Redraws are capped at 30 FPS. Rapid input is processed without drawing a
separate frame for every key, and idle screens do not redraw repeatedly.
Search messages are centered, and startup progress animates at 30 FPS.

Terminal checks cover output backpressure, slow service replies, rapid input,
resizing and visible key hints.

Firmware support adds USB DFU model profiles, Sitel wired devices and DECT
bases, wireless Engage updates, UC Voice headsets and PanaCast camera paths.
Engage 75 and 75 SE include all nine components and a standalone Bluetooth
flash program. Recovery checks preserve the selected archive and device
identity, and interrupted radio settings are completed on retry.

Engage 75 and 75 SE validation uses the simulator. No physical device was
available for those models. Pro 9460/9465/9470 and Pro 925/935 are outside
this release's added support.

Add an experimental `jabridge headset volume PERCENT` command for Evolve2 30 SE
(0b0e:0e36), firmware 1.11.0, connected directly by USB. It requests the headset's
own volume and records the request in debug history. The reporter in issue #44
confirmed that volume writes work on his headset and tested saved-level reads.
Saved readings can stay stale and differ from current playback. Other USB
variants have software test coverage and still need physical testing.
At its volume limits, the headset may also send a normal volume key to Linux.

Headset volume support now uses model, firmware and connection profiles. The
four Evolve2 30 SE USB layouts have separate checks. `headset volume get` reads
the saved level, and `headset volume info` shows supported operations without
sending a volume query. Debug reports include the playback profile, route state
and PipeWire volume to help investigate silent audio after reconnecting.

## 1.0.2 — 2026-09-15

Interactive startup now checks for a new Jabridge app version and asks whether
to update. Yes installs the signed release and restarts the same menu or
command. No, Enter or end of input continues without updating.

The TUI update screen accepts Y or N immediately and supports arrow-key
selection with No selected by default. The update screen and main menu have
a shared dark theme, mint highlights, softer borders and aligned menu items.
Fixed RGB colors also keep firmware, settings and status text readable across
terminal themes. Downloading and device startup stay inside the styled UI.

The update check has a two-second timeout. Failed checks do not block normal
use. Help, version output, JSON output, redirected streams, setup, service
commands and the background daemon do not show the prompt. The explicit
`jabridge update` and `jabridge update --check` commands keep their behavior.

## 1.0.1 — 2026-09-15

Fix Evolve2 40 firmware updates being rejected with a message asking for an
Engage 50 II. The shared Sitel updater now uses each model's runtime IDs,
bootloader ID and complete image set. Evolve2 40, Evolve2 40 SE, Evolve2 30,
Evolve2 30 SE and Connect 4h use their headset and sound-prompt images;
Engage 50 II keeps its separate controller image and activation step.

The menu, command line, debug report and recovery path use the same model
profiles. Other Sitel models report their own unsupported model instead of
asking for an Engage or falling through to the CSR updater.

Regression checks cover every added runtime variant, wrong-model rejection,
interrupted recovery and transfers of original firmware files through an
isolated test peer. These checks do not establish physical update or recovery
results for the newly added models. Other firmware families retain their
existing support limits.

Release builds and archive names now follow the application version, and the
signed application-update test accepts the actual release version. A new
codebase guide explains the main components and firmware flow.

## 1.0.0 — 2026-09-10

The native Go rewrite, renamed from jLink to Jabridge.

The CLI and terminal menu use a shared background service with local JSON-RPC
IPC. This release adds clearer device selection, scrolling settings, typed
choices, battery validation, supported controller settings and Bash completion.

PipeWire controls cover volume, mute and default audio devices. Supported
microphone activity can drive busylights. Optional media-button handling is
available, with safeguards against duplicate actions and interrupting calls.

Debug reports include access checks, device capabilities and private operation
history. Improved IPC handling covers cancellation, duplicate replies, disconnects
and replacement devices that reuse an ID.

App updates use signed release archives and refresh Bash completion even when
the service is stopped. Updates also refresh the installed binary when run from
a download folder. Device firmware uses separate
model/format checks and confirmation. The USB DFU release-checksum comparison
is corrected; CSR image CRCs and legacy chunk limits are checked before transfer.

Firmware installation uses sealed file snapshots and attachment-bound device
handles. CSR replies check their source, length and command echo, and early
events are kept in a bounded queue. Footer timeouts no longer imply success;
the selected USB port and installed version must be verified after reboot.

Firmware updates include native paths for supported Link, Speak and Evolve
models, plus new direct-USB previews for Evolve3 and Engage 50 II with Link Call
Control. Engage recovery can resume the controller step without repeating a
completed headset update. Controller downgrades are not supported.

A real Link 380 firmware reinstall passed. The newer Engage and Evolve3 update
paths passed software checks and still need physical update and recovery tests.
Support depends on the model and feature; this release does not claim every
Jabra device or firmware operation has been tested.
