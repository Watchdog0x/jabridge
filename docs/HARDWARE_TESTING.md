# Testing your device

## Link 390 connection and computer-audio controls

Preferred connection mode and computer audio have protocol, model and simulated
packet checks for Link 390 `2e56`/`2e57`, variant `04-10`, firmware `3.2.0`.
Neither control has a physical test report. Whether changing them interrupts
audio or disconnects a headset remains unverified. Other firmware profiles
do not expose them. Pairing and reset permissions are unchanged.

## Evolve3 65 controls preview

Power saving, noise-button cycle and five equalizer bands can be edited on
matching exact firmware profiles. No setting-test environment option is needed.
Power-saving and equalizer writes have no physical report yet.
USB voice-language updates have their own default-off option.
Software checks and recovery simulations do not qualify spoken language,
audible EQ or settings persistence. See TEST_EVOLVE3_CONTROLS.md and
TEST_VOICE_LANGUAGE.md for optional device tests.

Link 390 PID 2e56 list and search are off in 1.5.0 because their physical
checks were not reported. Pairing writes and every Link 390 reset remain disabled. Its earlier firmware
install/readback, sound, microphone and reconnect test passed according to
the tester; that does not qualify fresh pairing or the new controls.

On 2026-10-27 [@ggzengel reported](https://github.com/Watchdog0x/jabridge/pull/55#issuecomment-6436666392)
correct sleep-timer labels on Evolve3 65 (PID 2549, firmware 1.0.1) through Link
390 (PID 2e56, firmware 3.2.0). The working noise-button cycles were
HearThrough, ANC and Off; HearThrough and ANC; and HearThrough and Off. Other choices showed
errors; the report does not identify whether those were device errors or
readback differences, or qualify every ordering. Preview choices are narrowed
only for this exact setup. Other profiles show the full catalogued cycle and
current value; a row with no different allowed value cannot be edited.
Reported old mode sets do not qualify the
new menu, EQ or power-saving writes on every model.

## Link 380c 24c9 pairing

Issue [#58](https://github.com/Watchdog0x/jabridge/issues/58) originally reported
Link 380c `24c9`, variant `04-0C`, firmware `1.16.0` on openSUSE Leap 16.
The service and five dongle setting reads worked, but Jabridge blocked pairing.

On November 2, 2026, [@c-jung reported successful pairing](https://github.com/Watchdog0x/jabridge/issues/58#issuecomment-6573380991)
with an Evolve2 85 after following the 1.5.0 test steps. Since 1.6.1, list reads,
Classic search and fresh pairing are enabled without the old service option.
This report covers one setup; it does not establish reset, remembered-device
changes, settings writes, reconnect behaviour or support for every headset.
Those other writes remain disabled. See the [pairing guide](TEST_LINK380C.md).

## Tested devices

Testing a device does not mean every feature or firmware update has been tested.

| Device |
| --- |
| Jabra Link 380 |
| [Jabra Link 390](#link-390-pairing-and-dongle-write-test) |
| [Jabra Evolve2 55 (through Link 390)](#link-390-pairing-and-dongle-write-test) |
| Jabra Evolve2 65 |
| [Jabra Evolve2 85](#evolve2-85-noise-control-test) |
| Jabra Evolve3 85 |
| Jabra Speak 510 |
| [Jabra Speak2 75 (direct USB)](#speak2-75-usb-test) |
| Jabra Engage 50 II and Link Call Control |

Tested another device? [Open an issue](https://github.com/Watchdog0x/jabridge/issues/new)
with its name, what worked and your debug report so we can add it to this list.

## Link 390 pairing and dongle write test

On 2026-10-25 a Link 390 (`0b0e:2e57`, variant `04-10`, firmware `1.3.0`) was
tested with an Evolve2 55 (`0b0e:24f5`, variant `01-76`, firmware `1.2.13`) on
Ubuntu with Linux 7.0, using a build that adds USB ID `0b0e:2e57` to the
pairing-read and experimental-write allowlists.

The pairing-list read returned the remembered headset by name, and
`device.features` then reported `pairingList`. `bt.search` found the headset
within about one second and completed at 20 seconds. `bt.search.connect` paired
and connected it immediately, after which the headset was shown through the
dongle with battery and firmware reads. Auto pairing, Bluetooth radio and
softphone integration were read; the dongle rejected the
prioritize-computer-audio and dedicated-call queries, so those settings are not
listed. `bt.pair` returned success twice but did not pair on this dongle;
search and connect did.

Dongle writes checked on the same unit: `bt.disconnect` then `bt.connect` of
the remembered headset, each confirmed from the pairing list; auto pairing set
on and read back through `bt.autopair` and `settings.list`, then restored to
off, with the service recording a device acknowledgement and a matching
read-back; `bt.forget`, after which the pairing list was empty, followed by
re-pairing through search and connect.

`device.reset` with the confirmation string returned success. The dongle
disconnected from USB and re-enumerated, its HID interface registered again
12 seconds later, and the service had to be restarted to regain the control
interface. The pairing list afterwards still held one record, shown by
Bluetooth address instead of name, and then became unreadable. Two search and
connect attempts timed out with `headset did not connect before timeout`, and
enabling auto pairing did not pair within 45 seconds. After unplugging and
re-inserting the dongle and restarting the headset, the dongle connected the
headset on its own and search and connect restored the record name. Factory
reset was not verified as a clean reset in that test; a physical reconnect was
needed before pairing worked again. That build kept the Link 380-only reset
allowlist.

On 2026-10-26 the same tester compared Jabra Direct and Jabridge without
updating the dongle's 1.3.0 firmware. In two Jabra Direct tests, settings returned
to defaults and an address-only disconnected pairing record remained. Connecting
that record did not finish, but fresh search and Pair worked without a replug.
Jabridge left the same after-reset state; its fresh-search pairing timed out
until a physical reconnect. The leftover record alone is therefore not evidence
of a faulty Jabridge reset. The observed difference is recovery through pairing.
[The comparison](https://github.com/Watchdog0x/jabridge/pull/53#issuecomment-6423659083)
does not establish which command or timing difference caused it.

The candidate pairing flow scans and stops both classic Bluetooth and Bluetooth
LE, preserves the selected type, and binds connection checks to the same USB
attachment. Software tests cover the documented command masks, stale records,
timeouts, cancellation, missing events and device changes.

On 2026-10-27 [@shimonkohn confirmed the pairing fix](https://github.com/Watchdog0x/jabridge/pull/55#issuecomment-6446270226)
on the same Link 390 (2e57, variant 04-10, firmware 1.3.0) and Evolve2 55
(24f5, variant 01-76, firmware 1.2.13), using 1.5.0-preview.4. After a reset
with Jabra Direct, Classic search found the headset and pairing replaced the
address-only entry with a named connected entry, without unplugging USB.
The first search returned no results; the next search succeeded. That single
miss is unexplained and does not identify a device or software fault.
This qualifies the reported pairing recovery on that setup. Jabridge's own
reset route was disabled in that build; this test did not run it.
Use the [separate review-build instructions](TEST_LINK390.md) for optional
hardware checks.

Not verified: Bluetooth radio, softphone integration, prioritize-computer-audio
and dedicated-call writes, and firmware updates. Bluetooth radio and softphone
integration remain read-only on the reported 1.3.0 profile. The separate 3.2.0
model-backed settings below have software tests but no physical write report. The other
Link 390 USB IDs `0b0e:2e50` and `0b0e:2e51` remain outside the pairing
allowlist. ID `0b0e:2e56` list, search, pairing and remembered-device writes remain
disabled in this release.

### Experimental Link 390 firmware package

Issue [#54](https://github.com/Watchdog0x/jabridge/issues/54) reports a Link 390
`0b0e:2e56`, variant `04-10`, on firmware `3.0.13`. The official `3.2.0`
package contains one binary image, which the 1.4.0 installer rejects before
writing firmware. Its checksum and USB access checks passed in the report.

The candidate adds this monolithic protocol-7 format. The actual package has
passed a stateful software transfer test, along with fault and abrupt-process
interruption tests. [@ggzengel reported](https://github.com/Watchdog0x/jabridge/pull/55#issuecomment-6429435541)
a successful 3.2.0 installation and readback on PID 2e56. This establishes that
reported successful update, not physical interruption or recovery testing.
The candidate firmware path is off by default and requires this variable in
the installing process:

```sh
JABRIDGE_ENABLE_LINK390_CSR7_FIRMWARE=I_ACCEPT_UNTESTED_FIRMWARE
```

This does not enable settings, reset, pairing support for other USB variants,
or any other experimental control. Keep stable binaries separate and save a
debug report before hardware testing. Do not edit the archive or rename its
payload. Official release matching and live device protocol checks still apply.

Transfer-started recovery may restart only through an explicit recovery request
for the same archive and device. Once a commit may have begun, recovery only
checks the reported active version. An unknown result keeps the recovery record
and never repeats erase or commit. Jabridge's verification window is two minutes;
it is an observation limit, not a vendor-required delay. Unsuccessful runtime
checks have at least a one-second quiet interval before the next attempt.

Tests on a physical Link 390 must confirm the endpoint and language behavior,
complete installation, version readback, reconnect and interrupted-update
recovery before this path can be treated as supported.
The [review-build instructions](TEST_LINK390.md#firmware-package-issue-54)
describe the opt-in and its recovery risk.

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
The later physical mode-transition and complete-installation results are
described below. Power-loss recovery remains unqualified.

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
These mode-only tests qualify the transition on this headset; the subsequent
complete installation is recorded separately below.

#### Evolve 75 successful installation

On 2026-10-21 the owner ran `1.2.0+evolve75-detach-fix` and completed the
direct-USB update from `2.32.0` to `2.38.0`. The installer sent the firmware,
returned to normal mode, and reported that `2.38.0` was read back from the
device. Local history records successful transfer and verification, with
88.087 seconds from mode entry to completion. No manual resets or other
intervention were needed during that run.

The prompt said `RECOVER` because the failed mode-switch attempt had saved a
recovery record. That earlier attempt had sent no firmware blocks, so this
successful retry does not qualify recovery from an interrupted flash or power
loss. This is one complete successful installation on runtime ID `2465`; it
does not establish physical coverage for every Evolve 75 variant.

### Engage 75

Issue [#62](https://github.com/Watchdog0x/jabridge/issues/62#issuecomment-6664657865)
now has three matching docked component reports and one undocked report for
USB `1113`. All four components report firmware `4.4.1` directly:

| Component | Measured replies | Rejected queries |
| --- | --- | --- |
| Base 1 | PID `1113`, boot ID `1111`, variant `001b`, protocol 4, resources | None |
| Docked headset 10 | Serial present, variant `0148`, version, sound prompts | Product ID, boot ID |
| Display 3 | Variant `0304`, version | Product ID, serial, boot ID |
| Radio 2 | Version | None tested beyond version |

Undocking makes every headset query fail. A rejected headset product ID alone
therefore does not mean it is undocked. The catalog variant `01-48` matches the
headset, not the base; moving metadata discovery to the base alone would not
resolve the catalog mismatch. Settings permissions remain unchanged.

The candidate's read-only check has now run on hardware. Firmware installation,
recovery and old-firmware update compatibility remain unverified and blocked by
the missing identity replies. See [the read-only check](TEST_ENGAGE75.md).

The candidate adds an explicitly opt-in runtime GetInfo experiment. Software
fixtures cover the normal 33-byte management frames, strict 17/19-byte replies,
failure/cancellation, ownership and forbidden-command rejection. Physical support is
limited to the measured headset result described below; base and display still
reject it. It sends no restart or firmware-update request and does not change the
installer or its recovery checks. This is not an update qualification.

The physical result in [#62](https://github.com/Watchdog0x/jabridge/issues/62#issuecomment-6668692402)
now confirms runtime GetInfo image `0b0e:1116` on the headset, numeric mode 1,
decoded sector size 65536 and write block size 1001. Base and display reject
that query; base identity remains available through the original reads. The
raw sector/version representation and loader geometry are not established by
that report. New software tests preserve these distinctions and require every
Engage 75 image ID before preparation sends mode or area commands. Physical
entry, return and firmware installation remain unqualified.

Engage 75 and 75 SE firmware updates were tested in a simulator with the full
firmware package for both Bluetooth chip variants. Tests cover all nine
components, interrupted updates, recovery and final version checks.

Firmware transfers were not tested on a physical Engage 75 or 75 SE. Simulator
results do not establish physical flash timing or USB reliability. See [firmware coverage](https://github.com/Watchdog0x/jabridge/blob/main/docs/FIRMWARE.md)
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

### Link 390 model-backed configuration

Wireless range (`13/0e`: 1 normal, 2 low, 3 very low), softphone integration
(`13/4c`: 0/1), and ringer (`13/13`: selector 0, value 0/1) are enabled only
with exact `2e56`/`2e57`, literal variant `04-10`, firmware `3.2.0` model data.
Bluetooth radio is still read-only. The range and softphone flow sends one write
and one original-attachment END, then waits at least three seconds and requires
two fresh matching reads at least one second apart. These delays are software
policy, not measured hardware timing. Fault tests exercise actual GNP exchanges
and registry recovery using fake OS boundaries; they do not prove USB/GNP serial
equality, physical effects, or persistence after a later restart on real units.

### Link 390 reset in 1.6.0

The separate bound reset route is limited to USB `0b0e:2e57`, literal variant
`04-10`, firmware `1.3.0`. The older reports above establish the command and
USB restart on that profile. They do not establish a factory-clean database.
The current recovery flow has passed software tests only.

The request pins the selected device instance and sends one empty `0D/13`
command at the dongle address. It sends no configuration permission or END.
Registration records the kernel attachment generation; a stale confirmation is
refused before HID opens, including a fast replug with the same serial and node
name. Fresh handles must confirm the same identity on one new generation twice,
at least one second apart, within the 45-second return budget. The service then
publishes a new instance/path and drops stale headset and pairing caches.

A lost ACK stays unknown even if USB returns. Requested/unknown resets cannot
be replayed on that generation. An ACK with no confirmed restart is requested,
not verified. Pairing and range checks remain separate from this outcome.
Address-only records may remain, and an empty display list proves no erasure.
The strict reader cannot yet qualify an empty namespace's NAK or zero-address
reply, so incomplete summaries show unavailable. Range was not qualified as a
read on 1.3.0; an unsupported reply does not fail reset.

Software cases include denied/rejected operations, lost replies, delayed return,
wrong serial/firmware, renumbered nodes, stale targets, generation changes,
leases/search ownership and registry recovery without restarting the service.
They do not prove current-build physical timing or audible pairing recovery.
If the dongle stays missing, reconnect USB. Stop calls and audio before reset;
headsets may need pairing again. Link 380's older reset route is unchanged.

## Engage 75 issue #62 compatibility candidate

The latest physical report measured headset image `0b0e:1116` in Main mode.
The display rejected its runtime image request; no repeat report is needed.
The default-disabled compatibility executor now reads the display in the base
update transport, before any child transition or transfer. Its supported
reported modes are explicitly loader0/Main1; modes2/3 are refused.

Simulation is not physical qualification. The software models distinguish
runtime decoded65536/write1001 from freshly advertised loader geometry, and
check both radio revisions, identity failures, cancellation and replay guards.
A stored head serial cannot detect an unobservable same-model swap while the
base remains attached. Across-process dock continuity is lost, so neither the
pending journal nor V2 record permits automatic continuation. Different or
missing USB serials across modes are unresolved. No intact-old-display abort
or manual recovery procedure is claimed; physical use is still disabled.

## Engage 75 reconnect test, issue #62

[@mtalexan's 1.6.2-issue62.8 result](https://github.com/Watchdog0x/jabridge/issues/62#issuecomment-6746553895)
stopped at the update-mode reconnect identity check before the image sweep or
firmware transfer. [The follow-up](https://github.com/Watchdog0x/jabridge/issues/62#issuecomment-6782438972)
reports that removing base power for a few minutes restored its normal screen.
The missing boot serial is inferred from the native wait/binder difference;
the old result did not record which identity field failed. No physical firmware
transfer has been confirmed by this test.
