# Read-only hardware test

Thank you for testing Jabridge 1.0.0 RC5.

We need results from real Jabra dongles, wired headsets, wireless headsets, and
Link/controller devices. This first test only reads information. It must not
change your hardware.

## Do not run these actions

- Do not press Enter on a setting.
- Do not connect, disconnect, pair, or forget a headset.
- Do not press `2` for factory reset.
- Do not change sound or busylight settings.
- Do not run `jabridge firmware install` or a firmware `dev` command.
- Do not enable an experimental hardware-write environment variable.

## Download RC5

Download the Linux x86-64 archive, checksum, and signature from the
[v1.0.0-rc.5 preview](https://github.com/Watchdog0x/jabridge/releases/tag/v1.0.0-rc.5).

```bash
sha256sum -c jabridge_1.0.0-rc.5_linux_amd64.tar.gz.sha256
tar -xzf jabridge_1.0.0-rc.5_linux_amd64.tar.gz
cd jabridge_1.0.0-rc.5_linux_amd64
./jabridge --version
```

## Run the safe commands

```bash
./jabridge status
./jabridge battery
./jabridge settings
./jabridge model
./jabridge sound
./jabridge firmware
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
- `Remembered devices` lists each headset once.
- Dongle and headset settings show their current values.
- A long settings list scrolls with Up and Down.
- A setting with several choices shows its current choice.
- The firmware page shows the right target. Enter moves between targets when
  both a dongle and headset are available.
- The screen does not jump or flicker.
- `q` returns to the previous screen and quits cleanly from the home screen.

Turn the headset off and on, then unplug and reconnect its USB cable once.
Check that stale devices disappear and returning devices come back. Do not
press Enter on a device or setting during this read-only test.

The TUI still opens hardware directly. Do not run `jabridge --daemon` at the
same time.

## Permission denied

Install the included access rule if Jabridge cannot open `/dev/hidraw*`:

```bash
sudo install -m 0644 system/70-jabridge.rules /usr/lib/udev/rules.d/70-jabridge.rules
sudo udevadm control --reload-rules
```

Then physically unplug and reconnect the device. Run Jabridge as your normal
user, not with `sudo`.

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
download completed, but missing `hidraw` permission blocked device reads. RC5
adds clearer permission help and controller firmware probing. That headset
still needs a normal-user retest after installing the udev rule.

No Jabridge headset-setting write, remembered-device write, factory reset, or
firmware flash has passed a replaceable-hardware test yet.
