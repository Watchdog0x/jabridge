# Contributing to Jabridge

Thank you for helping maintain Jabridge.

## Development setup

Requirements:

- Go 1.23.2 or newer
- Linux for device-facing code
- PipeWire tools for meeting-detection integration tests

    git clone https://github.com/<your-username>/jabridge.git
    cd jabridge
    git remote add upstream https://github.com/Watchdog0x/jabridge.git
    make check
    make build

The project builds with CGO_ENABLED=0; no Jabra shared library is required.

The TUI and service currently open the device in separate modes. Do not run
them together. The planned design makes the service the only device owner and
the TUI a client of that service.

## Pull requests

1. Create a focused branch from main.
2. Add tests for changed behavior.
3. Run make check and make build.
4. Document the tested device model, VID:PID, connection type, and Linux
   distribution.
5. Distinguish unit/fake-transport results from real hardware results.

Do not commit firmware, vendor executables, shared libraries, device serial
numbers, logs containing personal data, or credentials.

Use [HARDWARE_TESTING.md](HARDWARE_TESTING.md) for safe, read-only device tests.

## Hardware-write safety

GNP commands and firmware updates can leave hardware unusable. New device-write
code must:

- fail closed unless its target device and firmware are identified exactly;
- use a fake or replay transport in automated tests;
- remain disabled by default until validated on replaceable hardware;
- include recovery and interruption tests before release;
- never infer support from a successful build alone.

The explicit development opt-in is not a safety certification:

    export JABRIDGE_ENABLE_EXPERIMENTAL_WRITES=I_ACCEPT_THE_BRICK_RISK
    export JAFW_ENABLE_HARDWARE_WRITES=I_ACCEPT_THE_BRICK_RISK

## Code style

- Run gofmt.
- Handle errors explicitly.
- Keep transport, protocol, and UI concerns separate.
- Preserve raw protocol captures as redacted test fixtures rather than device
  identifiers or personal serial numbers.
- Follow [Conventional Commits](.github/COMMIT_CONVENTION.md).

By contributing, you agree that your source contribution is licensed under
[Apache-2.0](LICENSE).
