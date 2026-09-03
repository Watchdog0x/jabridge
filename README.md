# jLink: Jabra Direct for Linux

[![CI](https://github.com/Watchdog0x/jLink/actions/workflows/ci.yml/badge.svg)](https://github.com/Watchdog0x/jLink/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/Watchdog0x/jLink)](https://github.com/Watchdog0x/jLink/releases)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://github.com/Watchdog0x/jLink/blob/main/LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Watchdog0x/jLink)](https://goreportcard.com/report/github.com/Watchdog0x/jLink)


## 🔧 Update Incoming!

Hey everyone just a heads-up:

An update for **JLink** is coming soon, and you’ll be able to upgrade your firmware easily.
I’ve completed the first step using the firmware updater. Here's a quick demo of updating my Jabra Evolve2 85 headset from version 1.3.8 to 1.5.4 (the latest):
```bash
./update ~/jabraFirmware/new/Firmware/Jabra_Evolve2_85-v1.5.4-ev285t-vector.zip 
  100% complete                              1.3.8 -> 1.5.4   [0b0e:24ba] Jabra Evolve2 85 UC Data
```



jLink is your go-to tool for managing **Jabra headsets and dongles** on Linux. Think of it as **Jabra Direct for Linux**. <br>
Now you can finally manage your Jabra devices on Linux with ease.
jLink brings the power of Jabra device management to your Linux system.

<div align="center">
  <img src="./src/image.png" alt="How jLink look" style="max-width: 100%; height: auto;">
</div>

## Features
    - Basic Control: Manage basic functions of your Jabra headset.
    - Device Discovery: Search for new devices and manage connections.
    - Paired Devices: View the list of paired devices.
    - Battery Status: Check the battery status of your headset

## Building from Source

### Prerequisites

- Go 1.23.2 or later
- GCC (C compiler for CGo)
- `xxd` (hex dump utility)
- `libasound2-dev` / `alsa-lib-devel`
- `libcurl4-openssl-dev` / `libcurl-devel`

**Ubuntu/Debian:**
```bash
sudo apt-get install build-essential libasound2-dev libcurl4-openssl-dev xxd
```

**Fedora/RHEL:**
```bash
sudo dnf install gcc alsa-lib-devel libcurl-devel vim-common
```

### Build

```bash
# Extract libjabra.so from install.sh (first time only)
make extract-lib

# Build the binary
make build

# Run
LD_LIBRARY_PATH=./lib ./jLink
```

See the [Makefile](Makefile) for all available targets (`make help`).

## Navigation

| Key             | Action                  |
|------------------|-------------------------|
| `w` or `↑`      | Move up                |
| `s` or `↓`      | Move down              |
| `Enter`         | Select an option       |

### Side Menu

| Key             | Action                  |
|------------------|-------------------------|
| `1`, `2`, `3`, `4` | Select an option      |
| `q`             | Go back                |



## Alternative: Build with setup-lib.sh

The Jabra SDK shared library is not committed to git (`lib/` is local only). You can also use `setup-lib.sh` to set up the build environment:

```bash
./setup-lib.sh          # creates lib/libjabra.so (or copies from /usr/lib/jabra if you ran install.sh)
go build -o jLink .
./jLink
```

On Fedora, `setup-lib.sh` also downloads a compatible `libcurl.so.4` (Ubuntu build with `CURL_OPENSSL_4`) because Fedora's system libcurl does not match the embedded Jabra SDK. Requires `curl`, `zstd`, and `ar` (binutils).

## Installation and Update

<div align="center">
  <img src="./src/install.png" alt="How jLink look" style="max-width: 100%; height: auto;">
</div>

### Option 1: RPM Package (Fedora/RHEL/CentOS)

Download the latest `.rpm` from [GitHub Releases](https://github.com/Watchdog0x/jLink/releases) and install:
```bash
sudo dnf install ./jlink-*.rpm
```

### Option 2: DEB Package (Ubuntu/Debian)

Download the latest `.deb` from [GitHub Releases](https://github.com/Watchdog0x/jLink/releases) and install:
```bash
sudo dpkg -i ./jlink_*.deb
sudo apt-get install -f  # resolve dependencies if needed
```

### Option 3: Using `curl`
Run the following command in your terminal:
```bash
curl -so- https://raw.githubusercontent.com/Watchdog0x/jLink/main/install.sh | sudo bash
```

### Option 4: Using `wget`
Run the following command in your terminal:

```bash
wget -qO- https://raw.githubusercontent.com/Watchdog0x/jLink/main/install.sh | sudo bash
```

## Tested Devices:

- jabra Link 380 with Jabra Evolve2 85

## TODO

    1. Code Cleanup: Improve the current codebase, which is in need of refactoring.
    2.  Device Switching: Add support for switching between multiple connected devices.
    3. ~~Headset Settings: Implement features for configuring advanced headset settings.~~ ✓ Done
    4. Sound Control: Integrate with PipeWire for precise sound management.
    5. Daemon Service: Create a background service using IPC shared memory for seamless operation

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details on:

- Development setup and prerequisites
- Coding standards
- Commit message conventions ([Conventional Commits](.github/COMMIT_CONVENTION.md))
- Pull request process

Quick ways to help:
  - **Refactor and Clean Up**: Improve the existing codebase.
  - **Implement New Features**: Tackle items from the TODO list.
  - **Report Bugs**: [Open a bug report](https://github.com/Watchdog0x/jLink/issues/new?template=bug_report.yml).
  - **Suggest Enhancements**: [Request a feature](https://github.com/Watchdog0x/jLink/issues/new?template=feature_request.yml).

## Community

- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Security Policy](SECURITY.md)
- [Changelog](CHANGELOG.md)


## Keywords
  - Jabra Direct Linux
  - Jabra headset Linux support
  - Jabra Linux command-line tool
  - Manage Jabra devices on Linux
  - Jabra Link 380 Linux
  - Jabra Evolve2 85 Linux

