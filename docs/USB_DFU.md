# USB DFU firmware preview

RC22 adds a shared native USB DFU updater. It does not need a vendor program,
shared library or dfu-util. The existing Link 380 updater remains separate.

The first model profiles are Speak 410, 510, 710 and 810. The same transfer
engine serves all four. Other devices can be added when their firmware format,
mode switch and recovery identity are known. A model name alone is not enough.

These are new hardware test paths. File checks and simulated transfers pass,
but actual installation and recovery on these four models are not confirmed.

## Try an update

Connect one supported device directly by USB. Do not use a Link route or
update during a call. Run setup once with the new binary so USB update mode
has access too. Run as your normal user; only setup may ask for a password.
Close other Jabra tools before starting. Jabridge pauses its service for the
transfer and prevents a new Jabridge device owner from starting until it ends.

```bash
jabridge setup
jabridge firmware download
jabridge firmware verify ./firmware/FILE
jabridge firmware install ./firmware/FILE
```

Replace FILE with the exact name printed by download. Speak 410 uses a raw
.dfu file. The other three checked releases use .zip files.

Installation checks the model, the official release checksum, the file's
target, its CRC and its format. It then asks you to type INSTALL. Leave the
device plugged in until it finishes. Success requires reading the expected
firmware version from the device after it returns to normal mode.

## If an update stops

Keep the same firmware file and use the same USB port. Jabridge saves a
private record before it sends the first update command. Running the same
install command again asks for RECOVER and starts the checked image again
from its first block. A previously checked file can be retried without an
internet connection when its saved hash, model and USB port match.

The normal debug history also records whether an update reached mode change,
transfer or final verification. It does not save firmware bytes or device names.

It does not guess a recovery device from its vendor ID. Only the documented
recovery ID on the selected port is accepted. This cannot recover a device
that no longer exposes the expected USB update interface.

## Scope and evidence

Speak 410 uses normal ID 0412 and update ID 0411.

Speak 510 uses normal IDs 0420 or 0422 and update ID 0421.

Speak 710 uses normal ID 2475 and update ID 0982.

Speak 810 uses normal ID 2456 and update ID 0971.

The USB vendor is 0b0e. Each runtime ID must also match Jabra's published
release checksum before a new transfer. Firmware for a different model,
ambiguous devices, unknown update IDs and changed recovery files are refused.
Permissions cover only these listed USB IDs.

The checked official files are Speak 410 version 1.12.0, Speak 510 version
2.32.8, Speak 710 version 1.40.0 and Speak 810 version 1.9.0. The parser checks
the CSR image header and the standard USB DFU suffix. It sends the image
without the suffix, using the transfer size reported by the device.

Tests cover block ordering, final empty block, short transfers, device errors,
cancelled or timed out operations, recovery from interrupted transfer states,
USB port matching, archive corruption and final firmware version mismatches.
Passing those tests does not replace a real device test.

Protocol references are the [USB DFU specification](https://www.usb.org/sites/default/files/DFU_1.1.pdf),
the [documented Jabra mode mappings](https://github.com/fwupd/fwupd/blob/main/plugins/jabra/jabra.quirk),
and the [DFU device behavior notes](https://github.com/fwupd/fwupd/blob/main/plugins/dfu/dfu.quirk).
Firmware files are not included in this repository.

The other firmware formats, including Engage controllers, Evolve3, Link 390
and PanaCast, still require their own native transfer support. They are not
sent through this USB DFU path.
