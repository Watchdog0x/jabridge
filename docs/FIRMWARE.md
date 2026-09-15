# Firmware implementation

A firmware protocol tells us how to send data. It does not prove that an
archive belongs to a device or contains the right images.

## Main files

| File in `internal/firmware` | Responsibility |
| --- | --- |
| `firmware.go` | Public commands, downloads and archive dispatch |
| `install_preflight.go` | Check a file before stopping the service |
| `interactive_install.go` | Bind the menu's selected file and USB device |
| `firmware_snapshot.go` | Freeze the selected archive for installation |
| `usb_attachment.go` | Keep device handles tied to the selected attachment |
| `recovery.go` | Save and load an interrupted transfer |
| `usb_dfu_profiles.go`, `jabra_usb_dfu_install.go` | Protocol 1 USB DFU models |
| `csr_release.go`, `csr_ota_updater.go` | Protocol 7 release matching and transfer |
| `csr_extended_install.go`, `csr_extended_update.go` | Protocols 16 and 17 |
| `sitel_profiles.go` | Protocol 4 model IDs and required images |
| `sitel_install.go` | Sitel update sequence and recovery |
| `sitel_runtime.go`, `sitel_hidraw.go` | Runtime identity and HID connections |
| `sitel_link.go`, `sitel_hid_frames.go` | Sitel messages and fragments |
| `sitel_images.go`, `sitel_flash.go` | Image bounds, transfer and CRC verification |
| `engage_controller.go` | Engage's optional Link Call Control activation |

## Sitel model profiles

These profiles describe implemented paths. They are not a list of physically
tested firmware updates.

| Family | Runtime USB PIDs | Bootloader PID | Ordered image targets |
| --- | --- | --- | --- |
| Engage 50 II | 4051, 4052, 4053, 4054, 4055, 4056 | 4050 | 3, 29, 27 |
| Evolve2 40 | 0E40, 0E41, 0E42, 0E43 | 0E44 | 3, 27 |
| Evolve2 40 SE | 2E40, 2E41, 2E42, 2E43 | 2E44 | 3, 27 |
| Evolve2 30 / Connect 4h | 0E30, 0E31, 0E32, 0E33, 0E35 | 0E34 | 3, 27 |
| Evolve2 30 SE | 0E36, 0E37, 0E38, 0E39 | 0E3A | 3, 27 |

USB IDs are hexadecimal. Image targets are decimal: 3 is headset firmware,
27 is its sound prompts, and 29 is the staged Engage controller image.

Runtime IDs come from Jabra's published product catalog. Bootloader IDs and
image order come from the matching official firmware manifests. Installation
also requires the current official release checksum and protocol for the
selected runtime device. A shared bootloader ID alone is not sufficient.

The installer reads the bootloader's flash areas, write size and image-header
offset. It validates every image before erasing anything, checks transferred
bytes with CRCs, then verifies identity and version after restart. It activates
Link Call Control only for Engage variants with that controller.

Recovery retains the archive digest, model, original USB port and device
identity. A different archive, device or model cannot inherit the record.

## Why issue 43 happened

The Evolve2 40 and Engage 50 II both use protocol 4. Version 1.0.0 treated every
Sitel archive as an Engage package, including its bootloader and three-image
requirement. That rejected the Evolve2 40's valid two-image archive.

The shared updater now reads one model profile across preflight, menu binding,
diagnostics, installation and recovery. Engage controller behavior remains
separate. Unimplemented Sitel layouts receive an explicit error.

## Validation and adding a model

`evolve2_sitel_test.go` covers the added runtime variants, interrupted recovery,
wrong identities, incomplete images and original-archive transfers through
an independent wire peer. `engage_install_test.go` and
`engage_evidence_test.go` retain Engage and controller checks.

For original files kept outside the repository:

```sh
JABRIDGE_TEST_SITEL_ARCHIVE_DIR=/path/to/firmware go test ./internal/firmware -run TestLocalEvolve2OriginalArchivesThroughNativeUpdater -v
```

Only add a profile after checking the official catalog and complete archive.
Verify runtime commands, image metadata and recovery behavior too. A model
with different targets, multiple device addresses, or another transfer format
needs that implementation before it can be enabled. Report simulated and
physical results separately.
