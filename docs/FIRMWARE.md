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
| `hid_access.go` | Read-only updater interface checks and retained failure reasons |
| `recovery.go` | Save and load an interrupted transfer |
| `usb_dfu_profiles.go`, `jabra_usb_dfu_install.go` | Protocol 1 USB DFU models |
| `csr_release.go`, `csr_ota_updater.go` | Protocol 7 release matching and transfer |
| `csr_extended_install.go`, `csr_extended_update.go` | Protocols 16 and 17 |
| `sitel_profiles.go` | Protocol 4 model IDs and required images |
| `sitel_install.go` | Sitel update sequence and recovery |
| `sitel_recovery_metadata.go` | Match update-mode devices to their saved runtime model and firmware |
| `sitel_runtime.go`, `sitel_hidraw.go` | Runtime identity and HID connections |
| `sitel_link.go`, `sitel_hid_frames.go` | Sitel messages and fragments |
| `sitel_images.go`, `sitel_flash.go` | Image bounds, transfer and CRC verification |
| `engage_controller.go` | Engage's optional Link Call Control activation |
| `sitel_ota_profiles.go`, `sitel_ota_runtime.go` | Wireless Engage images, parent and headset identities |
| `sitel_ota_install.go`, `interactive_wireless_install.go` | Wireless transfer, recovery and menu handoff |
| `sitel_dect_profiles.go`, `sitel_dect_runtime.go`, `sitel_dect_install.go` | Direct DECT base and docked headset updates |
| `bulk_camera_archive.go`, `bulk_usb_protocol.go`, `bulk_usb_linux.go` | Streamed camera archives, wire packets and USB bulk endpoint binding |
| `bulk_camera_runtime.go`, `bulk_camera_install.go` | Camera identities, activation, restarts and recovery |
| `uvc_camera_image.go`, `uvc_camera_linux.go`, `uvc_camera_transfer.go` | PanaCast 20 images and bound video extension controls |
| `uvc_camera_install.go` | PanaCast 20 component selection, restart and recovery |
| `panacast50_archive.go`, `gnp_file_transfer.go` | PanaCast 50 bundle integrity and GNP file transfer |
| `camera_mass_storage.go` | Bind, mount, copy, verify and unmount the camera's own firmware volume |
| `panacast50_runtime.go`, `panacast50_install.go` | PanaCast 50 mode changes, component readiness and recovery |
| `conexant_image.go` | UC Voice archive, S-record and packet validation |
| `conexant_hidraw.go` | UC Voice descriptor fields and bound HID handles |
| `conexant_transfer.go` | UC Voice calibration protection and verified writes |
| `conexant_install.go` | Official release checks, confirmation and recovery |

## Coverage in 1.1.0

The table lists implemented routes and how they were checked. It is not a
claim that every Jabra model has been physically tested.

| Protocol | Implemented in this tree | Evidence and remaining work |
| --- | --- | --- |
| 1, USB DFU | 20 bootloader profiles, 41 runtime IDs | Original archives and simulated transfers; physical qualification remains |
| 4, Sitel | 27 profiles, 111 runtime IDs, including Engage 45/65/75 and SE variants, Pro 9450 and Pro 920/930 bundles | Original image metadata and simulator coverage; Engage 75/75 SE use the standalone radio updater. Pro 9460/9465/9470 and Pro 925/935 are outside this release |
| 5, Conexant | UC Voice 150/250/550/750, IDs 0342 through 034F | Both packet formats checked against the original updater; simulated transfers; physical qualification remains |
| 7, 16, 17 | Existing CSR and extended transfer paths | Existing model support, with regression tests; this release does not add the excluded Pro models |
| 10 / 13 | PanaCast 50, three runtime IDs | Original bundle, three current release checks, independent GNP transfer, storage-path guards and simulated lifecycle; physical qualification remains |
| 11 | PanaCast 20, four runtime IDs | Original boot/main images through both page sizes, native header/CRC oracle, current release checks and simulated recovery; physical qualification remains |
| 12 | Engage 55 / 55 SE through Link 400 or a matching Engage base | Original archive, four catalog variants and simulated transfer/recovery; physical qualification remains |
| 18 | PanaCast 40 VBS and U30, six runtime IDs | Both original archives, live release checks, packet oracle and simulated restart/recovery; physical qualification remains |

Catalog entries alone do not enable an installer. Link 400's own firmware uses
its direct Sitel profile; its paired headset uses the separate wireless path.

## USB bulk camera updates

PanaCast 40 VBS uses archive PID 3080 and runtime PIDs 3081, 3085 and 3086.
PanaCast U30 uses archive PID 3093 and runtime PIDs 3092, 3093 and 3094.
IDs are hexadecimal. The installer checks both the exact profile and Jabra's
current release checksum for the attached runtime model.

These archives exceed one gigabyte. Payload validation and transfer stream the
data. The existing 256 MiB limit remains for headset archives; only known bulk
camera manifests may use a snapshot up to 2 GiB. Nested readers reuse the same
sealed file, and the menu rechecks the source hash without making another copy.
The Android payload size, header, full SHA-256 and metadata SHA-256 are checked.
These checks do not claim to authenticate the embedded vendor signature; the
official archive checksum is required separately before installation.

The updater claims only the active FF:CC:01 bulk interface. Endpoint addresses
come from USB descriptors; audio and video drivers are not detached. The wire
transfer checks every outgoing command CRC and confirms the staged archive's
MD5 before activation. Saved recovery binds the archive, USB port, model and
serial identity. Activation intent is saved before sending the command.
An uncertain activation reply never causes an automatic second activation.

The state machine waits for system update, reboot, library merge and any
subsystem update and second reboot. A new main version alone does not finish
the update. After completion, a fresh version is read from the same camera.
Recovery records the attachment before reboot so a process restarted after the
camera has returned does not wait for an extra reboot.

Tests include malformed payloads, exact native CRC vectors, independent packet
reconstruction, missing replies, changed identities, interruption, lost start
replies, one/two-reboot flows and failed checkpoints. Optional original-archive
tests use `JABRIDGE_TEST_BULK_CAMERA_AUDIT`; current catalog checks additionally
require `JABRIDGE_TEST_LIVE_RELEASES=1`. These checks are not physical camera
qualification.

## PanaCast 20 updates

PanaCast 20 uses video extension controls on the same captured USB attachment.
The updater checks the opened video handle, capture capability and control sizes.
Setup installs a model-specific video access rule. It also installs USB rules for
all registered DFU and bulk-camera IDs; tests compare those rules to the profiles.

The original archive contains a boot image and a main image. The boot image is
required only for the documented older product/version combinations; ordinary
updates send the main image. Each image gets a header containing its length,
destination, page size and CRC. Signed images retain their signature prefix,
while CRC calculation excludes that prefix as in the vendor updater. Jabridge
retains the signature bytes and checks the official whole-archive checksum;
it does not verify that signature itself.

The camera must accept the image header before any page is sent. Writes retain
the original 256/1024-byte page format and FF padding. Each failed USB request
stops the transfer. Vendor settling delays are retained before the next image or
restart; a delay alone never counts as completion. Recovery records each component
and the attachment before restart. The same camera must report the requested main
version after reconnecting. An uncertain restart reply does not trigger another
automatic restart, and a completed boot step is not repeated during a main retry.

`JABRIDGE_TEST_UVC_CAMERA_AUDIT` enables the original archive tests, and
`JABRIDGE_TEST_UVC_ORACLE` adds initialized header-field comparisons against the
original native helper. All four current runtime variants have passed live release
checksum checks. No physical PanaCast 20 has been flashed in this work.

## PanaCast 50 updates

The updater verifies the outer Jabra archive and the inner `upgrade.zip` checksums.
It uses the camera's GNP file interface when available, with ordered 55-byte data
blocks and a final device-reported size/MD5 check. Otherwise it uses the documented
USB storage mode. The storage path requires the UDisks2 system service and checks
the selected USB attachment, block device number, mount root and opened file.
Only `upgrade.zip` is written. Copies are synced and read back before unmounting;
a busy volume is not forcibly unmounted. See the [UDisks filesystem API](https://storaged.org/doc/udisks2-api/latest/gdbus-org.freedesktop.UDisks2.Filesystem.html).

Recovery keeps the chosen transport and every mode-change intent. Known unsent
commands remain retryable; uncertain activation/reset replies are monitored rather
than replayed. The main update, video processor update and final restart are tracked
separately. Success requires the original camera's normal USB identity, expected
version and component readiness. A confirmed rollback to older firmware permits an
explicit retry of the same archive. The original package, live release checks,
independent file receiver, storage-path guards and simulated failure/retry paths
have passed. Actual camera flashing and UDisks mounting remain untested on hardware.

## Wireless Engage updates

Engage 55 and 55 SE update through a Link 400 or a matching Engage 65/75 base.
The charging cable is not an update route. Jabra documents this for
[Engage 55](https://www.jabra.com/en-apac/supportpages/jabra-engage-55/9559-455-111/faq/686c1b80-7a19-4a6c-9e34-df12cb14e9ec)
and [Engage 55 SE](https://www.jabra.com/supportpages/jabra-engage-55se/9659-450-125/faq/0611763d-bdf2-4798-aa68-4624abb9b72f).

The service reads the paired headset serial and sends an identity digest to the
menu. A different headset with the same product ID gets a new selection token.
Before downloading, the menu captures the parent's USB attachment. After the
service stops, the installer reads both identities again and compares them
before confirmation and again before entering update mode.

Permission uses a mode-changing read request to the parent. It requires typed
confirmation and a saved recovery record. Battery, busy-link and existing-session
flags are checked first. Only a matching saved transfer may resume an active
wireless session. The parent stays in its normal USB mode; headset firmware
uses FWU address 4 and application-mode areas. The two images keep their own
versions and the sound-prompt region must match the headset.

All image areas and metadata are checked before erase. CRC checks guard writes
and completion. The installer leaves wireless update mode only after both
images verify, then reads firmware and sound-prompt versions from the same
headset. Recovery retains the exact archive, parent identity, child identity,
USB port and phase. A lost final reply can be retried without rewriting a
completed transfer. Failed transfers keep the record for an explicit retry.

The independent wire peer exercises the original archive, interrupted writes,
CRC failures, changed headsets, wrong image routes, denied permissions, low
battery, busy links, missing checkpoints and lost completion replies. These
are offline checks; they do not qualify a physical RF link or every hardware
revision. Reference artifacts remain outside the public repository.

## UC Voice updates

The installer requires the exact USB PID and the official protocol-5 archive
checksum. It binds the HID handle to the selected USB attachment and selects
the firmware fields from its descriptor. It prefers the Plus interface
(report 4) when available, otherwise the legacy interface (report 8).

Patch records stay in their original order, including repeated addresses.
The two interfaces encode addresses differently; the legacy calibration
comparison uses twelve address bits, while packet encoding retains the
original high address byte. Plus transfers split records around calibration
without losing or shifting the data beside it.

Before writing, the installer saves the archive, USB port, available serial
digest, HID descriptor digest and calibration range/digest. Interrupted
transfers reuse that original calibration range even if a partial patch has
already changed its pointer. Writes stop on disconnect, timeout, acknowledgement
mismatch or failed readback. Calibration is checked before the last patch write
and after transfer. A new firmware version must be read from the device before
the recovery record is removed. If a reconnect is needed, repeating the command
in the verification phase checks the version without rewriting firmware.

Older devices without a unique serial are bound by port, model, descriptor and
calibration. These checks cannot distinguish two physically swapped devices
whose observable identity and calibration are identical.

`conexant_*_test.go` covers malformed archives, wrong devices, HID fields,
calibration boundaries, ordered writes and injected failures. The optional
`JABRIDGE_TEST_CONEXANT_ORACLE` fixture compares every packet from all 14
official archives to the original managed updater methods and runs each archive
through an independent simulated peer in both modes. Reference binaries,
firmware and generated packet fixtures stay outside this repository.

## Sitel model profiles

Pro 920/930 adds a USB controller at FWU address 2 alongside the DECT base at 1
and docked headset at 10. Its binary image retains the vendor's fixed load base
and 512-byte FF padding. All three component areas and versions are checked before
erase. The headset boots before the USB controller, which restarts the base.
Recovery binds the USB controller identity as well as the base and headset.
The original package and all three runtime variants passed host checks and an
independent simulated transfer; physical flashing has not been qualified.

These profiles describe implemented paths. They are not a list of physically
tested firmware updates. The table shows the original release profiles;
`internal/firmware/sitel_profiles.go` contains the full development list.

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

The headset temporarily disappears from the audio panel while in update mode.
Keep it connected until the installer finishes. If the reboot reply is lost,
the updater checks for the expected new USB attachment before continuing. It
does not resend the reboot command. A retry uses the saved runtime model and
original firmware version, with both the published checksum and saved archive
hash checked. The debug report includes the saved recovery stage and firmware
stage history.

## Why issue 43 happened

The Evolve2 40 and Engage 50 II both use protocol 4. Version 1.0.0 treated every
Sitel archive as an Engage package, including its bootloader and three-image
requirement. That rejected the Evolve2 40's valid two-image archive.

The shared updater now reads one model profile across preflight, menu binding,
diagnostics, installation and recovery. Engage controller behavior remains
separate. Unimplemented Sitel layouts receive an explicit error.

The 1.1.0 follow-up exposed a retry failure: the download check used update-mode
PID `0e44`, which is absent from the model catalog. Recovery now looks up the
recorded runtime PID and original release. Tests also reproduce a lost reboot
reply leaving the headset in update mode; the new reconnect check handles that
case. The report does not establish which error stopped the tester's first
attempt, so confirmation on his headset is still needed.

## Validation and adding a model

`evolve2_sitel_test.go` covers the added runtime variants, interrupted recovery,
wrong identities, incomplete images and original-archive transfers through
an independent wire peer. `engage_install_test.go` and
`engage_evidence_test.go` retain Engage and controller checks.

`hid_access_kernel_test.go` adds opt-in Linux UHID tests for the real hidraw
open, handle-identity, descriptor and unnumbered-handshake paths. It requires
`JABRIDGE_TEST_UHID=1` and access to a loaded UHID driver. USB ancestry is supplied
by a synthetic test tree; this is not a physical USB reconnect or headset flash.
Normal tests also check that a timeout preserves the underlying access failure
without copying private paths or device identifiers into history.

When a supported device is already in firmware update mode, `jabridge debug`
uses the same interface-opening checks as the installer without starting its
protocol or sending a device command. The report includes the enclosing HID
collection page and a specific check result, such as `hid-identity`, `hid-layout`
or `hid-open-permission`. This is intended to diagnose the remaining #43 failure,
not to claim that the headset update has been fixed.

For original files kept outside the repository:

```sh
JABRIDGE_TEST_SITEL_ARCHIVE_DIR=/path/to/firmware go test ./internal/firmware -run TestLocalEvolve2OriginalArchivesThroughNativeUpdater -v
```

Only add a profile after checking the official catalog and complete archive.
Verify runtime commands, image metadata and recovery behavior too. A model
with different targets, multiple device addresses, or another transfer format
needs that implementation before it can be enabled. Report simulated and
physical results separately.

## Engage 75 and 75 SE

The archive contains nine components. They are installed in this order:
headset firmware, headset sounds, Bluetooth firmware, Bluetooth settings,
display firmware, base firmware, language files, graphics and base sounds.
Each component keeps its own version; sound files can have an older version
number than the base firmware in the same official release.

The radio updater reads the actual CSR8670 or CSR8675 identity and selects the
matching image and settings. An independently authored RAM program performs
internal flash operations. Only firmware sectors are used; reserved sectors
and sectors absent from the archive are preserved. Each transferred radio
sector is read back word for word. HEX components use device CRC checks.

Recovery records bind the archive, USB port, base and headset identities,
display and radio variant. An interrupted settings stage is replayed in file
order even when the radio already reports the new version. Assignments are
read back; deletions require the chip's successful command response. The base
and headset restart only after transfers and settings finish. Final checks
read each component's version.

Validation uses a simulator, including the original package's two radio
variants, HID and SPI packets, modeled flash operations, settings and recovery
faults. No Engage 75 or 75 SE was physically available. The simulator does not
establish real oscillator timing, analog flash behavior or USB hardware
reliability. No JabraCLI package, executable or vendor flash-loader blob is
needed at runtime.

Run the built-in Engage simulator checks without a device:

```sh
go test -race ./internal/firmware -run 'Test(Engage75|InternalFlash|SitelPSR)' -count=1
```

To test the complete original package, point the test at a downloaded Engage
75 firmware archive:

```sh
JABRIDGE_TEST_ENGAGE75_ARCHIVE=/path/to/Jabra_Engage_75_5.20.1.zip \
go test -race ./internal/firmware -run TestLocalEngage75WholeArchiveSimulation -count=1 -v
```

These tests use isolated peers and never open a physical device.
