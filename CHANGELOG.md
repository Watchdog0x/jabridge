# Changelog

This project follows [Keep a Changelog](https://keepachangelog.com/) and
[Semantic Versioning](https://semver.org/).

## [1.0.0] - Unreleased

### Added

- Native-Go Jabra USB and hidraw device layer.
- Jabridge daemon with JSON-RPC over a Unix socket.
- PipeWire meeting detection and busylight controller.
- Firmware catalog, download, format detection, manifest inspection, and exact
  attached-device PID verification.
- Live-validated Link 380 firmware-version, auto-pairing, and pairing-list reads.
- Experimental pure-Go CSR OTA state machine with fake-transport tests.
- One portable static `jabridge` executable that runs without an installation
  step or vendor runtime libraries.
- Explicit `jabridge update` command with platform matching, SHA-256 checking,
  safe archive handling, atomic replacement, and rollback on failure.
- Ed25519-signed application releases and signature verification during
  self-update.
- Bash completion for the full `jabridge` command tree.
- `jabridge battery`, with strict 0-to-100 validation and separate battery
  components when a model reports more than one battery.
- `jabridge settings`, with capability-probed dongle and headset settings,
  explicit values, and read-back after every change.
- `jabridge model`, backed by Jabra's current public device-model catalog.
- `jabridge models`, which summarizes and searches the full live Jabra model
  catalog without claiming catalog entries are hardware-qualified.
- A dated firmware-catalog audit covering every current Jabra USB PID and all
  98 unique latest firmware files without redistributing those files.
- `jabridge sound`, with guarded PipeWire output, volume, and mute controls.
- Device switching for multiple connected dongles and headsets.
- Editable remembered-device actions for connect, disconnect, and two-step
  forget operations.
- Headset choice settings for voice prompts, sidetone level, controller
  ringtone/volume, and supported programmable buttons.
- IPC subscriptions and device, battery, and pairing change notifications.
- Simple `jabridge ipc` commands and an IPC quick-start guide.
- The full IPC guide is published beside the portable archive as `IPC.md`, so
  older self-updaters keep accepting the archive's strict file layout.
- One-command `jabridge setup` device access, with an automatic first-run
  prompt, user installation, udev reload, connected-device retrigger, and an
  enabled service at sign-in.
- Simple `jabridge service start|status|stop|restart` commands.
- Desktop-session autostart and portable service control when a systemd user
  manager is unavailable.
- A user service and udev rule for non-root `hidraw` access.
- CI, community templates, and native package definitions.

### Changed

- Renamed the project from jLink to Jabridge to avoid the SEGGER J-Link package
  collision.
- Moved firmware status, download, verification, and experimental install into
  `jabridge firmware` so users need only one program.
- Removed CGo, Jabra headers, libjabra.so, and their runtime dependencies.
- Unsupported hardware operations now return errors rather than false success.
- Device state now uses synchronized snapshots and one cancellable refresh loop.
- Firmware status now shows installed and latest versions together.
- The TUI now composes each screen off-screen and writes one complete frame.
- Link 380 test mode can toggle auto-pairing and exposes a two-step,
  model-limited factory-reset command.
- Link 380 settings now include computer-audio priority, dedicated-call mode,
  Bluetooth radio, and softphone integration.
- Settings load without blocking the TUI, long lists scroll, and multi-choice
  values cycle with Enter.
- Firmware pages select an explicit dongle or headset target and reuse a valid
  cached download.
- The TUI now uses the service for all device state and actions, subscribes to
  events, keeps the connection alive, and reconnects after a service restart.
- Native firmware installation now uses a typed `INSTALL` confirmation. The
  `--i-accept-risk` option is reserved for automation, and the older flag
  remains a hidden compatibility alias.
- Added private interrupted-transfer state. Re-running `firmware install` with
  the exact same archive asks for `RECOVER` and replays it; a different archive
  or guessed changed PID is refused.
- Native firmware install now rejects non-CSR/GNP archive layouts before it
  saves recovery state or opens a device.
- Firmware downloads and cached files now have to match Jabra's published
  release checksum.

### Fixed

- Removed invalid battery readings above 100% by using validated Linux
  `power_supply` data instead of an unverified protocol byte.
- Removed duplicate remembered devices returned through multiple Bluetooth
  database types.
- Removed stale USB devices after unplug and reconnect.
- Prevented periodic TUI redraw flicker when device state has not changed.
- Prevented selected menu labels from moving sideways.
- Kept the same logical action selected when asynchronous device data inserts
  or removes menu entries.
- Preserved batched and split terminal key sequences instead of dropping keys.
- Added read-only discovery of a headset connected through a supported dongle.
- Added strict IPC parameter validation, idle timeouts, and connection limits.
- Added controller firmware probing for supported headset/controller pairs.
- Added value-aware Bash completion for every implemented setting.
- Published direct-USB devices to IPC before optional protocol enrichment, so
  models such as Speak 510 cannot disappear behind unsupported firmware-query
  timeouts.
- Made IPC device listing use cached metadata only and bounded initial device
  probes, keeping the service/TUI responsive for non-headset Jabra products.
- Added read-only multi-interface GNP endpoint discovery and `jabridge
  diagnose`, so a timeout is not mislabeled as a udev permission failure.
- Hid unavailable battery text from the TUI for wired headsets without a
  battery; the explicit battery command reports only `Battery: unavailable`.
- Restricted daemon socket and PID files to the current user and rejected
  unsafe filesystem paths.
- Accepted both standard ELF executables and PIE binaries during app updates.
- Recognized the current public Link 360, 370, 380, 390, and 400 USB IDs while
  keeping pairing and settings writes on their narrow test allowlists.
- Stopped labelling every non-dongle Jabra USB product as a headset in CLI
  inventory output; speakerphones, cameras, and room devices are identified.
- Required a descriptor-declared GNP output report when firmware code selects a
  HID interface; it no longer falls back to the first interface for a product.
- Accepted official UC/MS sibling firmware IDs only when their version and
  exact published release checksum match, fixing manifest-only false rejects.

### Safety

- Firmware writes require typed confirmation (or the explicit automation
  option). Destructive reset and forget actions require a second key press.
- Controlled change-and-restore tests passed for all five Link 380 settings.
  Every write was read back, and the original value was restored after each
  test. Factory reset was not executed.
- Headset setting writes, remembered-device writes, and firmware flashing are
  not release-qualified yet.
- Vendor binaries and firmware are not distributed.
- The 1.0.0 release remains blocked on real replacement-hardware validation.

## [0.1.1] - 2024-12-01

### Added

- Arrow-key navigation.
- Manjaro installer support.

### Fixed

- Installer version link and terminal prompts.

## [0.1.0] - 2024-11-01

### Added

- Initial jLink TUI and Jabra SDK integration.

[1.0.0]: https://github.com/Watchdog0x/jabridge/compare/v0.1.1...v1.0.0
[0.1.1]: https://github.com/Watchdog0x/jabridge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Watchdog0x/jabridge/releases/tag/v0.1.0
