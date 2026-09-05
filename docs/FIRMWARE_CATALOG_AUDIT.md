# Firmware catalog audit — 4 September 2026

This audit checked Jabra's current public model catalog and firmware service.
No firmware file is stored in this repository.

## Result

- 284 Jabra USB product IDs were present in the model catalog.
- 275 IDs had a public firmware response and at least one release.
- Those 275 responses resolved to 98 unique latest firmware files by Jabra's
  published MD5 checksum.
- The 98 unique files totalled 3.52 GiB.
- All 98 files were downloaded completely.
- All 98 matched the checksum in Jabra's model catalog.
- 91 were ZIP archives and passed a full ZIP integrity test.
- 7 were raw DFU files rather than ZIP archives.
- Every one of the 91 ZIP manifests reported the expected firmware version.

The complete per-file result, including SHA-256, is in
[firmware-catalog-audit-2026-09-04.tsv](firmware-catalog-audit-2026-09-04.tsv).
That report's SHA-256 is
`4fc4e732323cbdd2507458b62a80f78ff81bee8545ce1fe81a2f331574e0e654`.

## Firmware is not one format

The latest files contained these payload layouts:

| Payload layout | Unique files |
| --- | ---: |
| `.hex` and mixed legacy `.hex` packages | 29 |
| partitioned `.gnv` | 21 |
| raw or wrapped `.dfu` | 20 |
| `.ptc` | 14 |
| `.bin` and mixed legacy `.bin` packages | 9 |
| PanaCast VBS room-system bundles | 2 |
| PanaCast `.mvcmd` | 1 |
| mixed `.gnv` and `.dfu` | 1 |
| nested ZIP | 1 |

This matches the catalog's separate firmware protocol IDs 1, 4, 5, 7, 10,
11, 12, 16, 17, and 18. A protocol number alone is still not enough: Link 390
uses a `.bin` package under protocol 7, while Link 380 uses partitioned `.gnv`.
Jabridge therefore checks the archive payload structure before selecting a
native updater.

## USB IDs and manifest target IDs

The firmware service often uses several USB IDs for one byte-identical file.
For example:

- Link 380 USB-A IDs `24c7` and `24c8` receive the same checked file; its
  manifest names canonical target `24c7`.
- Link 380 USB-C IDs `24c9` and `24ca` receive another checked file; its
  manifest names canonical target `24c9`.

Across the 91 unique ZIP files, 48 representative firmware-service IDs differed
from the canonical target in `info.xml`. A manifest-only equality check would
therefore reject many correct official downloads.

Jabridge now verifies the local bytes against the checksum Jabra publishes for
the attached PID and firmware version. This accepts an official sibling ID only
when it resolves to the exact same release bytes. It does not use a broad or
guessed PID range.

## IDs without a firmware-service response

Nine catalogued IDs returned HTTP 404 from the public firmware endpoint:

- `0b0e:2e67` — Perform Go 4;
- `0b0e:3038` — PanaCast 50 VBS Bar;
- `0b0e:3040` — PanaCast 50 Video Bar System;
- `0b0e:3041` — PanaCast 55 VBS Bar;
- `0b0e:304c` — PanaCast 50 Video Bar System Touch Controller;
- `0b0e:304f` — PanaCast Control;
- `0b0e:3090` — Control IP and Scheduler profiles;
- `0b0e:3600` — PanaCast Reach;
- `0b0e:3601` — PanaCast USB Adapter.

This does not prove that the hardware has no update path. It only means the
public per-PID firmware endpoint did not offer one during this audit.

## Sources and method

The audit used:

- Jabra's public SDK model catalog at
  `https://cdn.cloud.jabra.com/models/v/16/product-group-bundles/bundles.json`;
- Jabra's public per-PID firmware service at
  `https://sdkbackend.jabra.com/v4/Firmware/PRODUCT_ID`;
- the download URL returned for each latest release;
- the MD5 release checksum published in the model catalog;
- each ZIP's `info.xml`, central directory, and full ZIP CRC test;
- a locally calculated SHA-256 for a stronger reproducibility identifier.

Duplicate PID endpoints with the same version and published checksum were
downloaded once as one unique firmware file. This avoids downloading identical
multi-gigabyte room-system images several times.

## Claim boundary

This proves catalog consistency and download integrity for that snapshot. It
does not prove that Jabridge can install all 98 files, that every listed device
is hardware-qualified, or that MD5 is a cryptographic signature. Firmware
installation remains enabled only for an explicitly recognized native payload
layout and a matching attached device.

Official product and command references:

- [JabraCLI supported devices](https://developer.jabra.com/sdks-and-tools/jabracli)
- [JabraCLI firmware commands](https://developer.jabra.com/sdks-and-tools/jabracli/reference)
- [Jabra Device Properties](https://developer.jabra.com/sdks-and-tools/device-properties)
