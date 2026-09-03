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
- A user service and udev rule for non-root `hidraw` access.
- CI, community templates, and native package definitions.

### Changed

- Renamed the project from jLink to Jabridge to avoid the SEGGER J-Link package
  collision.
- Moved firmware status, download, verification, and locked install tools into
  `jabridge firmware` so users need only one program.
- Removed CGo, Jabra headers, libjabra.so, and their runtime dependencies.
- Unsupported hardware operations now return errors rather than false success.
- Device state now uses synchronized snapshots and one cancellable refresh loop.
- Firmware status now shows installed and latest versions together.
- The TUI now composes each screen off-screen and writes one complete frame.
- Link 380 test mode can toggle auto-pairing and exposes a two-step,
  model-limited factory-reset command.

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
- Restricted daemon socket and PID files to the current user and rejected
  unsafe filesystem paths.
- Accepted both standard ELF executables and PIE binaries during app updates.

### Safety

- Hardware writes are disabled by default.
- A Link 380 auto-pairing change-and-restore test passed; the original setting
  was restored. Factory reset was not executed.
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
