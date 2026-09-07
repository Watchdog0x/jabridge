# Engage 50 II settings comparison

Based on the two RC19 reports and Jabra Direct screenshots shared in
[issue #5](https://github.com/Watchdog0x/jabridge/issues/5#issuecomment-5575331428)
on 7 September 2026. The screenshots show headset and controller firmware
4.1.3. They are a feature checklist, not defaults to apply.

## Fix startup first

Both reports show working HID/input access but `ExecMainStatus=218` and no
IPC service. The service failed before Jabridge could read settings. RC20
removes the incompatible user-service restrictions. Update, then run
`./jabridge service restart` as the normal user; no forced permission setup
is needed when access already passes. The affected host still needs a retest.

## What the screenshots show

"Definition exists" below means code exists, not that this headset has passed
read/write testing. A setting appears only after its model and native read
are accepted. Screenshot order alone does not prove numeric wire values.

| Controls in Jabra Direct | Jabridge coverage and remaining work |
| --- | --- |
| Call equalizer: Normal, Bass, Treble; audio protection: PeakStop, IntelliTone, G.616 | Definitions exist. Actual reads and reversible writes need testing. |
| IntelliTone level: 85, 82, 79 dB, available with IntelliTone protection | Level mapping and the dependent control are missing. |
| Sidetone on/off and +6, +3, 0, -3, -6, -9 dB | Definitions exist. Actual reads and reversible writes need testing. |
| Headset busylight, button sounds, mute reminder, left/right boom arm, music optimization | Definitions exist; some labels differ. Left/right uses the reverse-stereo setting. Confirm behavior, not only read-back. |
| Full mute control, including outside calls | Not mapped. Determine the required device/app behavior before implementing it. |
| Mute, hook, status and three-dot controller buttons; each offers Busylight, Call handling, Mute, Push-to-talk, Speed dial, No function | All four button definitions exist. Physical event/call-app behavior still needs testing; changing a setting does not prove integration. |
| Controller smart ringer; Ring, Happy, Melody; volume Off, Low, Mid, High | Definitions exist. Generic ringtone labels need a verified match to the named tones. |
| Headset name, controller name, two speed-dial numbers | First speed-dial definition exists. These two name properties and the second speed-dial property are missing. Text editing is CLI-only. |
| Ringtone in headset; call control with softphone | Headset ringtone definition exists. Softphone integration is currently dongle-scoped and must also support this headset/controller profile. |

The public profile also lists a forced-busy-state property without a native
mapping. The current UI does not yet reproduce Jabra Direct's headset,
controller and softphone grouping or its single Save action.

## Next hardware report

With the controller connected, run:

```bash
./jabridge service restart
./jabridge status
./jabridge settings
./jabridge debug --output engage-with-controller.txt
```

Repeat without the controller, using `engage-headset-only.txt`. For the
button/wheel check, use `./jabridge debug --guided --output engage-controls.txt`
and choose only controls this device has. Native setting reads, setting
writes, physical button events and meeting-app behavior are separate tests.
This startup fix does not qualify Engage firmware installation or recovery.
