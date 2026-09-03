# Security Policy

## Supported versions

| Version | Status |
| --- | --- |
| 1.0.x | Native-Go preview; security fixes accepted |
| 0.1.x | Legacy and unsupported |

## Reporting a vulnerability

Use a [private GitHub security advisory](https://github.com/Watchdog0x/jabridge/security/advisories/new).
Do not publish device serial numbers, firmware, proprietary vendor binaries,
credentials, or sensitive logs in a public issue.

Please include:

- affected Jabridge version and commit;
- device model and VID:PID, with serial numbers redacted;
- whether the issue is read-only, a device write, or a firmware update;
- reproducible steps using a fake/replay transport when possible;
- impact and any known recovery path.

## Device safety

Protocol writes and firmware updates can make hardware unusable. A passing unit
test, CI build, or simulated transport does not establish hardware safety.
Hardware-write features remain disabled by default until independently
validated on replaceable devices.

Jabra SDKs, Device Connector, firmware, and updater vulnerabilities should also
be reported to Jabra through its official security process.
