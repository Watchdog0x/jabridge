# Testing your device

## Tested devices

Testing a device does not mean every feature or firmware update has been tested.

| Device |
| --- |
| Jabra Link 380 |
| Jabra Evolve2 65 |
| Jabra Evolve2 85 |
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

## Firmware simulator tests

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

The same reporter reproduced silent audio after reconnecting with Jabridge
fully disabled on kernel 7.2.6 and reports normal playback on 6.18 LTS. The
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
