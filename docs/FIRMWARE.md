# Firmware update and recovery

Jabridge treats firmware support as model-specific. It does not assume every
Jabra headset, speakerphone, controller, dongle, or dock uses the same update
protocol.

## Information Jabridge checks

Before an update, Jabridge checks:

1. USB vendor and product ID.
2. The firmware archive's target PID list.
3. Product name and firmware version in the archive manifest.
4. Firmware file format.
5. The archive uses the supported partitioned `.gnv` layout, including CRCs
   and a final partition. Archives for other updater protocols are refused.
6. The HID report descriptor before sending any management query.

Jabra's documentation says supported properties and behavior vary by exact
model, variant, and firmware. Jabridge applies the same rule to firmware:

- [Jabra Device Properties](https://developer.jabra.com/sdks-and-tools/device-properties)
- [JabraCLI supported devices](https://developer.jabra.com/sdks-and-tools/jabracli)
- [JabraCLI firmware commands](https://developer.jabra.com/sdks-and-tools/jabracli/reference)

The full latest-file check is recorded in the
[firmware catalog audit](FIRMWARE_CATALOG_AUDIT.md).

## Install

Download and verify first:

```bash
jabridge firmware download --pid PRODUCT_ID
jabridge firmware verify ./firmware/FILE.zip
```

Start the update:

```bash
jabridge firmware install ./firmware/FILE.zip
```

Jabridge shows the product, version, and target PID. Type `INSTALL` to continue.
Anything else cancels before a firmware-transfer command is sent.

The `--i-accept-risk` option skips the typed prompt for deliberate automation.
It does not skip archive, format, or target verification.

The current native transfer path is for the partitioned CSR/GNP `.gnv` archive
layout used by Link 380 and several protocol-7 headsets. Protocol 7 alone is
not enough: Link 390 uses a `.bin` package, and one older headset package mixes
`.gnv` and `.dfu`. A public Engage 50 II protocol-4 archive instead contains
controller, headset, and tune-pack `.hex` payloads. All of those different
layouts are rejected before a device is opened. Firmware protocols 1, 4, 5,
10, 11, 12, 16, 17, and 18 require separate implementations as well.

Use `jabridge model` for the attached model's published firmware protocol, or
`jabridge models MODEL_NAME` to inspect the live catalog.

## Automatic recovery retry

Before the first firmware-transfer command, Jabridge writes a private state file
containing only:

- archive SHA-256;
- product name;
- firmware version;
- target PIDs;
- attempt number and time.

No serial number or Bluetooth address is stored.

If the update process fails or is interrupted, the state remains. Run the same
install command with the exact same archive:

```bash
jabridge firmware install ./firmware/FILE.zip
```

Jabridge detects the unfinished state, shows the same target, and asks for
`RECOVER`. Recovery replays the complete archive from the beginning. A different
archive is refused. The state file is removed only after the transfer completes.

This follows the user-level recovery flow documented by Jabra Direct: keep the
failed device connected/listed and retry its update with Recover. Jabra warns
not to disconnect during update or recovery:

- [Jabra firmware update and recovery instructions](https://www.jabra.com/ro-ro/supportpages/jabra-biz-2300/2399-829-189/faq/how-do-i-manually-update-the-firmware-on-my-jabra-device-using-jabra-direct)
- [Jabra Engage 50 II recovery instructions](https://www.jabra.com/en-emea/supportpages/jabra-engage-50-ii/5099-299-2269/faq/ecf56073-fd74-4adf-9cbe-569f90222a3b)

## What recovery does not guess

Jabridge currently requires an attached device whose PID matches the archive.
If a failed device re-enumerates with a different recovery or DFU PID, Jabridge
stops. It will not assume an unknown dock, controller, or DFU node belongs to
the archive.

Changed-PID recovery can be added only from a documented or hardware-validated
normal-PID to recovery-PID mapping for that exact model and firmware protocol.

If the exact target no longer appears, use the official recovery flow or Jabra
support. Jabra's guidance also says to contact support if its recovery update
does not succeed.

## No random device commands

Discovery reads HID report descriptors first. Only interfaces declaring the
known GNP management output report are eligible for read-only identity, variant,
and firmware queries. Audio, button, consumer-control, and unrelated dock
interfaces are skipped.

Device-write commands are allowlisted and require an explicit user action.
Jabridge does not fuzz opcodes or send configuration, reset, pairing, or
firmware blocks to discover capabilities.

## Qualification status

- Link 380 firmware files and target matching: passed.
- Update and downgrade using the older known-working updater: owner-reported
  pass.
- Jabridge native full transfer: record only after the exact command is
  confirmed.
- Interrupted Jabridge transfer and automatic recovery retry: fake-transport
  and state tests only; real spare-device test still required.
- Headsets, controllers, docks, and speakerphones: model-specific testing still
  required.
