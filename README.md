# Jabridge: Linux control for Jabra devices

Jabridge is a Linux tool for supported Jabra headsets and USB dongles. Version
1.0.0 is a new Go rewrite of the old `jLink` project. It does not use CGo or
`libjabra.so`.

Download the compiled file and run it. You do not need to install Jabridge,
Go, GCC, .NET, Node.js, the Jabra SDK, or Jabra Device Connector.

> [!WARNING]
> Version 1.0.0 is still a hardware test preview. Safe, read-only checks pass on
> one Link 380 dongle. Its five settings below also passed controlled
> change-and-read-back tests. Real-headset settings, remembered-device changes,
> factory reset, and firmware flashing still need replaceable-hardware testing.
> Firmware flashing stays locked.

The new name avoids confusion with SEGGER J-Link, which also uses the `jlink`
name on Linux.

![Jabridge TUI showing a Link 380 with no headset connected](src/image.png)

The TUI uses a high-contrast theme. The picture shows the tested Link 380-only
state; it is not proof of headset support.

## Download and run

Download the Linux x86-64 archive and checksum from
[Releases](https://github.com/Watchdog0x/jabridge/releases).

```bash
sha256sum -c jabridge_*_linux_amd64.tar.gz.sha256
tar -xzf jabridge_*_linux_amd64.tar.gz
cd jabridge_*_linux_amd64
./jabridge --version
./jabridge status
./jabridge firmware
./jabridge
```

Jabridge is one static file. You can copy it to another compatible Linux
computer and run it there.

Linux must still allow your user account to open the Jabra `hidraw` device. If
you get a permission error, install the included access rule, then reconnect
the USB device:

```bash
sudo install -m 0644 system/70-jabridge.rules /usr/lib/udev/rules.d/70-jabridge.rules
sudo udevadm control --reload-rules
```

Unplug and reconnect the headset, controller, or dongle after installing the
rule.

DEB and RPM packages install this rule automatically. Do not run the whole app
as root.

## Update Jabridge

Check for a newer app release:

```bash
./jabridge update --check
```

Install the latest app release:

```bash
./jabridge update
```

Testers can include release candidates with `./jabridge update --prerelease`.
The command verifies the release signature and checksum, then replaces the
`jabridge` app. It never updates a headset or dongle.

## Bash completion

Enable completion in the current Bash session without installing anything:

```bash
source <(./jabridge completion bash)
```

The source installer, DEB, and RPM install the same completion files for future
shell sessions.

## How the TUI and service work

There are two run modes today:

- `./jabridge` opens the terminal UI (TUI). It talks directly to the device.
- `./jabridge --daemon` starts the background service. It also talks directly
  to the device and gives other apps a local JSON-RPC socket.

Firmware commands are part of the same program under `./jabridge firmware`.

The TUI does **not** use the service yet. Run the TUI or the service, not both
at the same time. The planned design is for the service to own the device and
for the TUI to connect to the service.

PipeWire is optional. The service uses it only to detect meetings and control
automatic busylight behavior.

The service already has a private Unix-socket API for device, battery, pairing,
and settings events. Moving the TUI and CLI onto that API is still in progress.

## What works now

- Finds supported Jabra dongles and direct-USB headsets. It also probes for a
  headset connected through a supported dongle.
- Shows a clear dongle-only screen when no headset is connected.
- Shows and switches between multiple connected devices.
- Shows battery data only when it is between 0 and 100. Separate left, right,
  and case values are kept when a headset reports more than one battery.
- Reads Link 380 firmware, saved pairing records, and these five settings:
  auto pairing, computer-audio priority, dedicated-call mode, Bluetooth radio,
  and softphone integration.
- Changes those five Link 380 settings and reads each value back before saying
  it worked.
- Shows only headset settings that answer on the connected model. This includes
  on/off settings and multi-choice settings such as voice prompts, sidetone
  level, controller ringtone, and programmable buttons.
- Lets a tester connect, disconnect, or forget a remembered Link 380 headset.
  Forget and factory reset both require a second key press.
- Scrolls long settings lists and keeps movement/change controls on the right.
- Keeps menu text and the selected action still while device data changes.
- Checks and downloads firmware for an explicitly shown dongle or headset
  target. Existing valid downloads are reused.
- Provides simple `battery`, `settings`, `model`, and `sound` commands.
- Runs as a TUI or a local background service.
- Builds as one static Linux program.
- Updates the app with an explicit signature- and checksum-checked command.
- Includes Bash completion for all commands.
- Tests firmware-update code with fake devices.

## What still needs testing

- Real headsets connected by USB or through a dongle.
- Headset battery components, power-off/on, unplug, and reconnect behavior.
- Every model-specific headset setting and controller-button mapping.
- Remembered-device connect, disconnect, and forget operations.
- Factory reset, pairing, and busylight changes.
- PipeWire sound writes on different desktops.
- TUI and CLI operation through the background service.
- Interrupted firmware updates and recovery.
- Firmware flashing on replaceable test hardware.

No Jabra program, shared library, or firmware file is included in this project.

## Simple commands

```bash
./jabridge status
./jabridge battery
./jabridge settings
./jabridge model
./jabridge sound
```

Change a setting by copying its name from `jabridge settings`:

```bash
./jabridge settings set dongle.auto-pairing on
./jabridge settings set headset.voice-prompts voice
./jabridge settings set headset.three-dot-button push-to-talk
```

Jabridge hides settings that the connected device does not answer. Boolean
settings use `on` or `off`. Other settings use one of the choices allowed by
the matching public model profile. If that profile cannot be loaded, choice
settings stay read-only. Bash completion suggests known names and values; the
connected model still decides what is accepted.

## Firmware

Firmware tools are built into Jabridge. Check the attached device and latest
available firmware:

```bash
./jabridge firmware
./jabridge firmware download
./jabridge firmware verify ./firmware/FILE.zip
```

When more than one firmware target is present, the CLI asks for an exact
product ID, for example `firmware download --pid 24c7`. In the TUI, the
firmware page shows `Target 1 of N`; press Enter to move to the next target.

`verify` checks that the firmware file and attached device have the same product
ID. It does not install the firmware.

`jabridge firmware install FILE` exists for controlled development but stays
locked in this preview. Do not enable it for community testing.

## Build from source

Only developers building the program need Go 1.23.2 or newer and
golangci-lint 2.13.2 or newer:

```bash
make check
make build
```

The compiled program is `dist/bin/jabridge`.

## Help test Jabridge

Read [HARDWARE_TESTING.md](HARDWARE_TESTING.md) and report your result in
[hardware test issue #34](https://github.com/Watchdog0x/jabridge/issues/34).
Never post a device serial number or Bluetooth address.

For code changes, read [CONTRIBUTING.md](CONTRIBUTING.md). Passing CI proves the
software builds and its automated tests pass. It does not prove a hardware
write is safe.

Report security problems privately as described in [SECURITY.md](SECURITY.md).

## Independent project

Jabridge is an independent community project. It is not made, approved, or
supported by GN Audio A/S. Jabra is a trademark of GN Audio A/S. Product names
are used only to say which hardware may be compatible.

Jabridge source is licensed under [Apache-2.0](LICENSE). Third-party tools,
libraries, and firmware are not redistributed here.
