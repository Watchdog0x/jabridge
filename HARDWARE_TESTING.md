# Read-only hardware test

Thank you for testing Jabridge 1.0.0 RC11.

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

## Download RC11

Download the Linux x86-64 archive, checksum, and signature from the
[v1.0.0-rc.11 preview](https://github.com/Watchdog0x/jabridge/releases/tag/v1.0.0-rc.11).

```bash
sha256sum -c jabridge_1.0.0-rc.11_linux_amd64.tar.gz.sha256
tar -xzf jabridge_1.0.0-rc.11_linux_amd64.tar.gz
cd jabridge_1.0.0-rc.11_linux_amd64
./jabridge --version
```

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

An Engage 50 II with its Link controller was detected by RC3 and its firmware
download completed, but missing `hidraw` permission blocked device reads. RC11
adds clearer permission help and controller firmware probing. That headset
still needs a normal-user retest after installing the udev rule.

No Jabridge headset-setting write, remembered-device write, factory reset, or
firmware flash has passed a replaceable-hardware test yet.
