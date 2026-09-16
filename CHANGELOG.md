# Changelog

## 1.1.0 — 2026-09-16

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
own volume and records the request in debug history. Original firmware
emulation passed; physical headset feedback for issue #44 is still pending.
At its volume limits, the headset may also send a normal volume key to Linux.

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
