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

## Application update trust

`jabridge update` accepts a release only when all three checks pass:

- the archive matches its SHA-256 checksum;
- the archive matches GitHub's release-asset digest when one is present;
- the archive has a valid Ed25519 signature from the Jabridge release key.

Release-key SHA-256 fingerprint:
`076bde06762651011f53c8727db097d4129af9fb9fbbfaa3fe9da51fe7d45788`.

The signing key is not used for headset or dongle firmware. Device firmware
remains under the separate hardware-validation rules below.

## Device firmware downloads

Jabra's public model catalog publishes an MD5 checksum for each device firmware
release. Jabridge downloads through HTTPS and requires the bytes to match that
published checksum before reusing or installing the file. MD5 is useful here
as Jabra's release-identity and corruption check, but it is not a modern
cryptographic signature. Jabridge also records SHA-256 in its dated catalog
audit for reproducibility.

Some official UC/MS sibling USB IDs share one byte-identical firmware file even
when its internal manifest names only one canonical PID. Jabridge accepts that
relationship only when the attached PID, version, and published checksum all
resolve to the exact same official release. It does not accept a wildcard PID
range.

## Device safety

Protocol writes and firmware updates can make hardware unusable. A passing unit
test, CI build, or simulated transport does not establish hardware safety.
Firmware writes require an exact typed `INSTALL` confirmation; an unfinished
retry requires `RECOVER`. The
`--i-accept-risk` option exists only for deliberate non-interactive automation.
This acknowledges the risk of a wrong or interrupted updater command; it does
not imply that the correct model-matched firmware file is unsafe. Link 380
setting writes are limited to the settings that passed a change-and-read-back
test. Destructive reset and forget actions require a second confirmation.
Headset settings, pairing writes, factory reset, and interrupted-transfer
recovery are not release-qualified until independently tested.

Jabra SDKs, Device Connector, firmware, and updater vulnerabilities should also
be reported to Jabra through its official security process.
