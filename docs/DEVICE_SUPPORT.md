# Device support

Jabridge does not call every device with a Jabra logo "supported". It uses four
clear levels:

1. **Catalogued** — Jabra publishes a model profile for the USB ID.
2. **Detected** — Jabridge sees the real device on Linux.
3. **Readable or editable** — the exact operation works and is read back.
4. **Hardware-qualified** — a person tested that operation on the real model.

A catalog match is useful, but it is not a hardware test.

## Current result

As of 4 September 2026, Jabra's live SDK model catalog contains:

- 198 Jabra product profiles;
- 125 Jabra product groups;
- 449 Jabra variant records;
- 284 different Jabra USB product IDs;
- firmware protocol IDs 1, 4, 5, 7, 10, 11, 12, 16, 17, and 18, plus
  products with no public firmware protocol ID;
- 12 partner-device profiles that do not use Jabra's USB vendor ID.

Run this at any time to read the current catalog instead of relying on those
snapshot numbers:

```bash
jabridge models
jabridge models Link
jabridge models Evolve2
jabridge models Engage
jabridge models Speak
jabridge models PanaCast
```

Jabridge can catalogue and identify far more models than it can safely change
today. The only hardware-qualified device is currently one Link 380 USB-A
(`0b0e:24c7`). Other devices need real-hardware reports.

## Product families

| Family | What is possible now | What is still needed |
| --- | --- | --- |
| Link 380 | USB detection, model and firmware reads, saved-headset reads, and five dongle settings work on `0b0e:24c7` | Test USB-C and other variants; finish new-headset scan events |
| Link 390 | Catalogued and eligible for safe discovery | Real hardware tests before pairing or settings writes |
| Link 400 | Catalogued, but it is a DECT dongle rather than the Link 380 Bluetooth path | A separate tested DECT pairing implementation |
| Evolve, Evolve2, Engage, Speak, Speak2, Perform | USB identity and exact online model profiles can be used; common read operations are candidates | Test each protocol family and model before enabling writes |
| Engage 40, Engage 50, Engage 50 II controllers | Exact model profiles expose controller settings and multiple-choice button functions | Real controller tests for every read and write |
| Evolve3 | Catalogued with newer firmware protocols 16 and 17 | New protocol work and real hardware; do not reuse the protocol-7 updater |
| Biz, Pro, UC Voice and older Link devices | Catalogued legacy devices | Tests for firmware protocols 1, 4, and 5 and older control layouts |
| PanaCast 20, 40, 50, 55 and U30 | Catalogued camera and room-device profiles | Camera controls and firmware protocols 10, 11, and 18 are separate work |
| Video Bar System, Control IP and Scheduler | Catalogued appliances or controllers | They are not ordinary headset HID devices and are outside the current native headset path |

Jabra's current JabraCLI list names Evolve/Evolve2/Evolve3, Engage, Perform,
PanaCast 20, Speak/Speak2, PanaCast 50 USB, PanaCast U30 USB, and PanaCast 40
VBS USB. This is useful evidence that Linux management is possible for those
families, but it does not mean they share one wire protocol:

- [JabraCLI supported devices](https://developer.jabra.com/sdks-and-tools/jabracli)
- [Jabra Device Properties explorer](https://developer.jabra.com/sdks-and-tools/device-properties)
- [Jabra Linux integration options](https://developer.jabra.com/sdks-and-tools/linux)

## Pairing without a dongle button

Jabra's Link 380 instructions use software for pairing: put the headset into
pairing mode, open the pairing screen, search, select the headset, and connect.
The official Link 380 flow does not document a physical pairing-button step.
The current SDK also exposes software pairing for Link 380 and Link 390, and a
separate DECT flow for Link 400:

- [Link 380 support and pairing](https://www.jabra.com/supportpages/jabra-link-380)
- [Jabra device-pairing API](https://developer.jabra.com/sdks-and-tools/dotnet/device-pairing)
- [Official Link 380/390 pairing sample](https://github.com/gnaudio/jabra-dotnet-bt-pairing-sample)

That makes new-device pairing an important Jabridge feature. Saved-device
connect, disconnect, and forget operations exist, but scanning for a new
headset is not ready. Jabridge still needs a validated parser for the dongle's
asynchronous search-result events. It will not guess event layouts or send
unknown commands to hardware.

## Settings and buttons

Settings vary by exact product, variant, and firmware. Jabridge therefore uses
the live model profile to hide choices that do not belong to the attached
model. A profile alone does not reveal every low-level USB command, so a new
setting is enabled only after its command and read-back behavior are known.

Jabra officially lists button and LED customization for Engage 40, Engage 50,
and Engage 50 II. It includes press, tap, double-tap, button-down, button-up,
and LED control. These are model-specific features, not a promise for every
headset:

- [Jabra button customization](https://developer.jabra.com/sdks-and-tools/javascript/button-customization)
- [Official device-properties samples](https://github.com/gnaudio/jabra-dotnet-device-properties-sample)

## Firmware

The catalog proves that one universal firmware routine cannot cover all Jabra
hardware. Jabridge's current native transfer path was built for the
partitioned `.gnv` GNP/CSR layout used by the tested Link 380 path. Even some
other protocol-7 products use a different payload layout. Protocols 1, 4, 5,
10, 11, 12, 16, 17, and 18 must be treated as separate implementations until
proven otherwise.

See [Firmware](FIRMWARE.md) for target checks, confirmation, retry behavior,
and the current recovery boundary.

## How support will grow

For each model or protocol family, Jabridge will add support in this order:

1. identify the exact USB ID, variant, device type, and firmware protocol;
2. detect the correct HID management interface from its descriptor;
3. run read-only identity, firmware, battery, and setting tests;
4. test one reversible write and read it back;
5. test reconnect, power-off, and USB-unplug behavior;
6. only then mark that operation hardware-qualified.

Test reports belong in
[the hardware testing issue](https://github.com/Watchdog0x/jabridge/issues/34).
Never post a serial number or Bluetooth address.
