# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- CI/CD pipeline with GitHub Actions (lint, build, release)
- RPM packaging for Fedora/RHEL/CentOS
- DEB packaging for Ubuntu/Debian
- Package validation in CI (rpmlint, lintian, container install tests)
- Dependabot for Go modules and GitHub Actions updates
- Makefile with build, lint, test, and install targets
- golangci-lint configuration
- Contribution templates (bug report, feature request issue forms)
- Pull request template with checklist
- Conventional commits guide
- CONTRIBUTING.md with development setup instructions
- CODE_OF_CONDUCT.md (Contributor Covenant v2.1)
- SECURITY.md with vulnerability disclosure policy
- This CHANGELOG

## [0.1.1] - 2024-12-01

### Added
- Arrow key navigation support (up/down arrows in addition to w/s keys)
- Manjaro Linux support in the installer
- Installer improvements for running without root privileges

### Fixed
- Version link in installer script
- curl/wget installer prompts with /dev/tty

## [0.1.0] - 2024-11-01

### Added
- Initial release
- Terminal User Interface (TUI) for Jabra device management
- Jabra SDK integration via CGo bindings
- Device discovery (automatic dongle and headset detection)
- Bluetooth pairing (search, connect, disconnect, remove devices)
- Battery status monitoring with visual bar
- Paired device list management
- Dongle settings (auto-pairing toggle, factory reset)
- Installation script with embedded libjabra.so
- Support for Ubuntu, Debian, Fedora, CentOS/RHEL, Arch Linux

[Unreleased]: https://github.com/Watchdog0x/jLink/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/Watchdog0x/jLink/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Watchdog0x/jLink/releases/tag/v0.1.0
