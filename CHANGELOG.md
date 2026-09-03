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
- Portable static `jabridge` and `jafw` executables that run without an
  installation step or vendor runtime libraries.
- Explicit `jabridge update` command with platform matching, SHA-256 checking,
  safe archive handling, atomic replacement, and rollback on failure.
- Bash completion for `jabridge` and `jafw`.
- CI, community templates, and native package definitions.

### Changed

- Renamed the project from jLink to Jabridge to avoid the SEGGER J-Link package
  collision.
- Renamed the independent firmware utility from `jabrafwu` to `jafw` so it is
  not mistaken for an official vendor program.
- Removed CGo, Jabra headers, libjabra.so, and their runtime dependencies.
- Unsupported hardware operations now return errors rather than false success.

### Safety

- Hardware writes are disabled by default.
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
