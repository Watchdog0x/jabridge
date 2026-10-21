# Testing your device

## Tested devices

Testing a device does not mean every feature or firmware update has been tested.

| Device |
| --- |
| Jabra Link 380 |
| Jabra Evolve2 65 |
| [Jabra Evolve2 85](#evolve2-85-noise-control-test) |
| Jabra Evolve3 85 |
| Jabra Speak 510 |
| [Jabra Speak2 75 (direct USB)](#speak2-75-usb-test) |
| Jabra Engage 50 II and Link Call Control |

Tested another device? [Open an issue](https://github.com/Watchdog0x/jabridge/issues/new)
with its name, what worked and your debug report so we can add it to this list.

## Speak2 75 USB test

[@mj-crabtree reported no issues](https://github.com/Watchdog0x/jabridge/issues/46)
using Speak2 75 over direct USB on Pop!_OS 22.04 LTS with Jabridge 1.1.0.
The device reports USB ID `0b0e:24ef` and firmware `2.54.0`.

The debug report confirms device detection, battery and settings reads, and
volume-button and hook-switch events. It does not verify setting writes,
firmware installation, or use through a dongle.

## Evolve2 85 noise control test

[@vstoms tested Off, ANC and HearThrough](https://github.com/Watchdog0x/jabridge/pull/48)
on Evolve2 85 (`0b0e:24bb`, variant `01-67`, firmware `1.5.7`) through Link 380
(`0b0e:24c8`, firmware `1.16.0`). The contributor reports checking the returned
value and hearing each mode change. Direct USB, the mixed HearThrough mode and
HearThrough level were not tested by this contribution.

## Evolve2 40 firmware status

The firmware test in [issue #43](https://github.com/Watchdog0x/jabridge/issues/43)
remains unresolved. Jabridge `1.1.1-test.2` opens the update interface but times
out before transferring firmware. This model must not be treated as having a
verified complete firmware update based on the simulator or interface tests.

## Link 370 USB DFU recovery

A Link 370 with runtime ID `0b0e:245e` and DFU ID `0b0e:0976` was
updated from `1.82.0` to `1.87.0` during local testing on 2026-10-21.
Jabridge 1.2.0 stopped twice and required manual completion:

- After the mode switch, the new USB node appeared in sysfs before the active
  user's uaccess ACL was applied. A separate mode-switch diagnostic reproduced
  about 425 ms of `EACCES` before the same node became accessible.
- All 1,056 data blocks and the zero-length final block were accepted, but the
  following GETSTATUS exceeded the USB request's two-second timeout. A later
  status read reported success in `dfuIDLE`. Resetting returned the device to
  DFU `appIDLE`; a second reset restored its normal USB identity. The device
  then reported firmware `1.87.0` and the paired headset was detected again.

The access wait now retries only transient node-open failures, while checking
the same port and DFU product ID. Finalization retries only GETSTATUS timeouts
within the existing 60-second deadline; it does not resend firmware blocks or
treat a timeout as success. Normal-mode firmware version verification remains
mandatory. Menu-launched installer failures now remain visible in the terminal
with the complete recovery instructions.

Regression tests cover delayed permissions, cancellation, permanent access
errors, final-status timeouts, device errors after a timeout, and the two-reset
return to normal mode. The patched access path also passed a physical
mode-switch test without firmware transfer. A complete unattended update with
these changes and recovery after power loss have not been qualified.

## Firmware update tests

### Evolve 75 USB DFU review

The official Evolve 75 `2.38.0` archive was checked on 2026-10-21 against
Jabra's protocol-1 release metadata for runtime IDs `2465`, `2466` and `2467`.
Its bootloader target is `097a`; this is a direct USB update, not a wireless
update through Link 370. The complete 1,901,624-byte payload passed archive,
header, suffix and CRC validation, menu/CLI preflight, and a simulated transfer
with 1,858 data blocks of at most 1,024 bytes followed by the zero-length block.

A physical Evolve 75 connected by USB as `0b0e:2465` passed a read-only runtime
preflight: firmware `2.32.0`, management interface 3 with report 5 and 64-byte
input/output reports, USB-node access, opened-device descriptor validation, and
menu binding to the verified archive. No interface was claimed or detached,
and no reset, update-mode command or firmware block was sent.

Review of the shared USB DFU finalization path found that it kept polling after
a non-tolerant device reported `dfuMANIFEST`. Under
[USB DFU 1.1 sections A.2.8 and A.2.9](https://www.usb.org/sites/default/files/DFU_1.1.pdf),
that device can stop answering class requests after its advertised poll interval
and require a reset. The updater now waits that interval before returning to the
existing reset and version-verification path. A timeout or stall alone does not
establish success; this path requires a successful `dfuMANIFEST` status and the
descriptor's `bitManifestationTolerant` flag to be clear. Tolerant devices continue
to be polled.

Regression tests reproduce the previous stall, check the poll interval and
cancellation, and reject the old runtime version after reset. These are software
tests, not an observed Evolve 75 hardware failure or proof of a complete update.
The headset's complete installation, physical flash timing and power-loss
recovery remain unqualified. Mode-transition testing is described below.

Further review found that recovery rejected devices still completing a data
block (`dfuDNLOAD-SYNC` / `dfuDNBUSY`) or finalizing an image
(`dfuMANIFEST-SYNC` / `dfuMANIFEST` / `dfuMANIFEST-WAIT-RESET`). Recovery now
honors the pending poll interval before aborting an incomplete download. For
pending manifestation it finishes waiting, resets and verifies the installed
version without sending firmware again. Errors or cancellation stop recovery;
an old runtime version still fails verification. This uses the device's current
DFU state; it does not identify a previously completed update once the device
has already returned to idle or normal mode.

A timeout on the update-mode command also previously stopped the installer
without checking whether the command took effect. It now waits within the
existing discovery deadline for the expected bootloader on the original USB
port, without resending the command. Tests cover both outcomes, an unrelated
bootloader on another port, cancellation, and immediate failure for other
errors. These recovery and mode-switch findings were reproduced in simulators;
they have not been exercised by interrupting a physical Evolve 75 update.

#### Evolve 75 detach timeout: physical reproduction

A subsequent update attempt on firmware `2.32.0` entered `0b0e:097a`, then
returned to normal mode before sending firmware. The updater consequently
timed out waiting for the same-port bootloader. A mode-only diagnostic reproduced
the failure and restored the original firmware without sending any DNLOAD.

The first `097a` interface set is a composite Bluetooth/DFU runtime: `btusb`
binds interfaces 0 and 1, while DFU uses interface 2 with protocol 1, attributes
`0x07`, a 5,000 ms detach maximum, a 1,023-byte transfer size and DFU version
`0x0101`. Jabridge ignored that detach maximum and requested only 1,000 ms.
Resetting the USB device took 2.619 seconds; the detach window expired and the
headset returned to normal `2465` on firmware `2.32.0`.

Requesting the advertised 5,000 ms window instead reached `097a` with protocol
2 on interface 0 and successful `dfuIDLE` status. The reset took 2.483 seconds.
No kernel drivers were manually unbound and no firmware blocks were sent.
Cleanup resets restored normal mode and firmware `2.32.0` was read back.

The updater now requests `wDetachTimeOut` from the descriptor, as specified by
[USB DFU 1.1 sections 4.1.3 and 5.1](https://www.usb.org/sites/default/files/DFU_1.1.pdf).
Regression tests reproduce a delayed reset, enforce smaller advertised maxima,
preserve self-detaching behavior, and parse the captured runtime descriptors.
This qualifies the mode transition on this headset, not a complete firmware
installation or interrupted-flash recovery.

### Engage 75

Engage 75 and 75 SE firmware updates were tested in a simulator with the full
firmware package for both Bluetooth chip variants. Tests cover all nine
components, interrupted updates, recovery and final version checks.

No physical Engage 75 or 75 SE was available. These results do not establish
physical flash timing or USB reliability. See [firmware coverage](https://github.com/Watchdog0x/jabridge/blob/main/docs/FIRMWARE.md)
for the other update paths and their test limits.

## How to test

Use your device normally. Try sound, microphone, buttons and settings. Then save a report:

```bash
./jabridge debug --output jabridge-debug.txt
```

Check the report before sharing it. [Open an issue](https://github.com/Watchdog0x/jabridge/issues/new), attach the file and tell us your device model, how it is connected and what worked or failed.

You do not need to update firmware or reset your device to test it.

## Firmware recovery

1. Keep everything plugged in. Retry with the **same firmware file** (replace `FILE.zip`):

   ```bash
   ./jabridge firmware install ./firmware/FILE.zip
   ```

2. Type `RECOVER` when asked. Leave everything plugged in until it finishes. If it fails or recovery is unavailable, stop and [send a debug report](#how-to-test).

## Evolve2 30 SE volume test

Hardware testing has focused on USB product 0b0e:0e36 with firmware
1.11.0. USB request handling, all 16 internal levels, and the digital gain path
have passed execution of the original firmware in a bounded emulator. This
models USB, storage, scheduling and DSP boundaries; it is not an audible test.

The reporter in issue #44 has now confirmed that writes change audible volume
on 0b0e:0e36 with firmware 1.11.0 and has tested saved-level reads, including
readings that remain stale after a write. The additional 0e37/0e38/0e39 descriptor
profiles have software evidence only.

The same reporter reproduced silent audio with Jabridge fully disabled.
On his setup, 6.18 LTS avoids the kernel mixer problem, but a hub connected
through the NVIDIA USB controller can still cause silent playback on LTS.
Connecting that hub to a motherboard rear USB port works for him. The
[upstream USB audio change](https://kernel.googlesource.com/pub/scm/linux/kernel/git/tiwai/sound/+/3c87a903a6820a0789bbab61681dd570d6030260)
keeps a mixer available when its volume readback is unreliable. These results
describe this tester's setup; they do not establish the cause of every audio
problem on other systems.

The test build needs the new USB access rule from `jabridge setup`. Keep Linux
volume steady while testing `jabridge headset volume 50`. A write response means
the headset accepted the request. Its saved value can lag behind the active
volume. Send the latest `jabridge debug` report with the result; it includes
history, including the requested volume percentage.

Expanded firmware emulation also followed the USB feedback path. Two firmware
host-volume modes emitted a normal volume key when reaching an internal end
level. Jabridge does not call PipeWire for this command, but it does not suppress
those device-generated keys. Check both the audible result and Linux's volume
when testing. This behavior is retained in the emulator evidence.

`jabridge headset volume info` shows support without querying volume. The
explicit `get` command reports the saved value and labels it accordingly.
It cannot establish current loudness or successful playback. The debug report
now also records the standard analog/digital profile, playback node/link state,
and PipeWire percentage without private stream or profile names.
