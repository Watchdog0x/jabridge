# Read-only hardware test

Thank you for testing Jabridge 1.0.0 RC16.

We need results from real Jabra dongles, wired headsets, wireless headsets, and
Link/controller devices. This first test only reads information. It must not
change your hardware.

## Do not run these actions

- Do not press Enter on a setting.
- Do not connect, disconnect, pair, or forget a headset.
- Do not press `2` for factory reset.
- Do not change sound or busylight settings.
- Do not run `jabridge firmware install` or a firmware `dev` command.
- Do not type `INSTALL` or `RECOVER`, and do not use `--i-accept-risk`, during
  this read-only test.
- Do not enable an experimental hardware-write environment variable.

## Download RC16

Download the Linux x86-64 archive, checksum, and signature from the
[v1.0.0-rc.16 preview](https://github.com/Watchdog0x/jabridge/releases/tag/v1.0.0-rc.16).

```bash
sha256sum -c jabridge_1.0.0-rc.16_linux_amd64.tar.gz.sha256
tar -xzf jabridge_1.0.0-rc.16_linux_amd64.tar.gz
cd jabridge_1.0.0-rc.16_linux_amd64
./jabridge --version
```

The main report command is `./jabridge debug --output report.txt`.
RC15 and newer automatically save a private action/error history. If something
happens in the TUI, run debug afterward, even after closing/restarting the app. The
report includes recent events, not just the current device state. Previous
RC14 TUI activity cannot be recovered. After updating, run
`./jabridge service restart` once to install the new service unit.
In a terminal it includes a 20-second button window; follow the prompt and
press one device control at a time. `--buttons=false` skips observation.
The report includes all exposed model settings and choices, native read
results, HID field layout/activity, firmware metadata/cached-file checks,
and sanitized service/IPC failures. Unknown fields remain unknown.

For Speak 510, collect the report on the host; optionally collect a second
inside Distrobox with another filename. For Evolve3, test direct USB and the
Link route separately. Native flashing for protocols 1, 16 and 17 is not
implemented; a firmware download or matching checksum does not qualify it.

For Engage 50 II, please run `./jabridge setup` once as your normal user.
Only the access-rule step asks for administrator permission. If setup fails,
continue with `./jabridge debug --output engage-with-link.txt` and share that
file. Repeat without the Link controller using `engage-headset-only.txt`.
The report works even when the service cannot start. Native tests are then
marked BLOCKED. With the service running, RC13 records each native firmware,
battery and setting read, plus catalog properties the diagnostic does not
cover. PASS means that specific read passed, not that every feature works.
Audio quality, calls and hardware writes remain separate manual tests.
It does not include
serial numbers, Bluetooth addresses, usernames, or raw logs.

After setup, `./jabridge buttons` listens for 20 seconds. Press the headset
buttons and turn the wheel. Please share which events appear. Normal button
actions still work during this check. Buttons that Linux does not expose may
produce no events; this is not full vendor-button or call-app integration.

## Run the safe commands

```bash
./jabridge status
./jabridge battery
./jabridge diagnose
./jabridge settings
./jabridge model
./jabridge models "Evolve2 65"
./jabridge sound
./jabridge firmware
./jabridge service status
./jabridge ipc ping
./jabridge update --check --prerelease
source <(./jabridge completion bash)
./jabridge
```

In the TUI, check these items without changing them:

- The dongle, headset, and controller names are correct.
- A direct-USB headset works without a dongle.
- A headset connected through a dongle is detected.
- `Switch device` lists every connected device when more than one is present.
- Battery values stay between 0 and 100. Left, right, or case batteries remain
  separate when the headset reports them separately.
- A powered wireless headset reports battery through its Link dongle.
- `Remembered devices` lists each headset once.
- Dongle and headset settings show their current values.
- Evolve2 65 shows its supported boom-arm, audio-protection, auto-sleep,
  button-sound, mute-reminder, sidetone, sound-mode, firmware-lock, ringer, and
  computer-audio settings.
- A long settings list scrolls with Up and Down.
- A setting with several choices shows its current choice.
- The firmware page shows the right target. Enter moves between targets when
  both a dongle and headset are available.
- A headset through a Link dongle shows its installed firmware instead of
  `Unknown`.
- The screen does not jump or flicker.
- `q` returns to the previous screen and quits cleanly from the home screen.

Turn the headset off and on, then unplug and reconnect its USB cable once.
Check that stale devices disappear and returning devices come back. Do not
press Enter on a device or setting during this read-only test.

The TUI uses the background service. While the TUI is open, run
`jabridge service restart` in another terminal once and check that the TUI
reconnects without losing its current screen.

If the same headset is available through both direct USB and a Link dongle,
also run these connection choices. They do not write headset settings, but they
do change the default PipeWire output and microphone to follow the selected
path:

```bash
./jabridge use dongle
./jabridge battery
./jabridge service restart
./jabridge battery
./jabridge use usb
./jabridge battery
```

The two dongle battery reads should agree, showing that the selected connection
survives a service restart. Every reported percentage must remain from 0 to
100. If a route has no battery, the CLI should say only `Battery: unavailable`.

## Permission denied

Run the one-time setup:

```bash
./jabridge setup
```

Approve the normal password prompt. The command installs the included access
rule, reloads it, and retriggers connected devices. Reconnect USB once only if
Jabridge asks. Run Jabridge as your normal user, not with `sudo`.

## Send your result

Reply in [issue #34](https://github.com/Watchdog0x/jabridge/issues/34) with:

- dongle, headset, and controller model;
- USB VID:PID, but no serial number;
- direct USB, through a dongle, or dongle-only connection;
- Linux distribution, `uname -r`, and `uname -m`;
- output from the safe commands above;
- what changed after headset power off/on and USB reconnect;
- any wrong value, duplicate, delay, jump, flicker, or error.

Never post a serial number or Bluetooth address.

## What has passed so far

One Link 380 (`0b0e:24c7`) passes discovery, firmware reads, model matching,
saved-pairing reads, firmware-file matching, reconnect checks, and all five
current setting reads. Controlled tests changed and restored each of its five
settings with read-back verification.

RC11 Engage 50 II testing confirmed USB detection and PipeWire enumeration:
`4052` with the Link controller and `4056` with the headset alone. Both expose
one USB device. Installed firmware and settings remained unavailable, and
setup timed out starting the service. The cause is not confirmed. RC12 adds
access/report diagnostics, service exit details and descriptor framing fixes
for a focused retest.

No Jabridge headset-setting write, remembered-device write, factory reset, or
firmware flash has passed a replaceable-hardware test yet.
