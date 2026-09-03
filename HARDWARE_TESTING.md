# Read-only hardware test

Thank you for testing Jabridge 1.0.0.

We need results from different Jabra dongles, headsets, and Linux systems. This
test reads device information. It must not change your device.

## Do not run these actions

- Do not flash firmware.
- Do not start or remove Bluetooth pairing.
- Do not reset a device.
- Do not change busylight settings.
- Do not run `jabridge firmware install` or developer firmware commands.
- Do not enable either experimental hardware-write setting.

## Run the safe test

Download the Linux x86-64 archive and checksum from the
[v1.0.0-rc.1 preview](https://github.com/Watchdog0x/jabridge/releases/tag/v1.0.0-rc.1).

```bash
sha256sum -c jabridge_1.0.0-rc.1_linux_amd64.tar.gz.sha256
tar -xzf jabridge_1.0.0-rc.1_linux_amd64.tar.gz
cd jabridge_1.0.0-rc.1_linux_amd64
./jabridge --version
./jabridge status
./jabridge firmware
./jabridge
```

The last command opens the TUI. Check that it shows the right dongle and
headset. Unplug and reconnect the device once. Check that the TUI recovers.
Then exit without choosing an action that changes the device.

The TUI opens the device directly. Do not run `jabridge --daemon` during this
test.

## Send your result

Reply in [issue #34](https://github.com/Watchdog0x/jabridge/issues/34) with:

- dongle and headset model;
- USB VID:PID, but no serial number;
- direct USB, dongle, or dongle-only connection;
- Linux distribution;
- output of `uname -r` and `uname -m`;
- output of `jabridge status` and `jabridge firmware`;
- what the TUI showed before and after reconnecting the device;
- any error, hang, flicker, or wrong device name.

Never post a serial number or Bluetooth address.

## What has passed so far

One Link 380 (`0b0e:24c7`) passed device discovery, firmware-version reads,
auto-pairing reads, saved-pairing reads, firmware-file matching, and reconnect
checks. No headset was available. No firmware was flashed and no setting was
changed.
