# Changelog

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
