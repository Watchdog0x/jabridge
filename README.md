# Jabridge

Jabridge controls supported Jabra headsets, Link controllers, and USB dongles
on Linux. It is one small terminal app with both a simple menu and commands.

You do not need Go, C, .NET, Node.js, a Jabra SDK, or a vendor library.

> [!WARNING]
> Version 1.0.0 is still a hardware-test preview. A Link 380 is tested. Real
> headsets and controllers still need more testing. Firmware installation is
> available with exact target checks and typed confirmation; interrupted-update
> recovery is not qualified yet.

## Start in three steps

1. Download the newest Linux archive from
   [Releases](https://github.com/Watchdog0x/jabridge/releases).
2. Extract the archive.
3. Open a terminal in the extracted folder and run:

```bash
./jabridge
```

That opens the menu. If Linux access is missing, Jabridge asks:

```text
Jabridge needs one-time access to your headset. Set it up now? [Y/n]
```

Press Enter and approve the normal password window. Reconnect the USB device
once only if Jabridge asks. Do not run the whole app with `sudo`.

The first setup also installs Jabridge for your user and enables its background
service. The service starts automatically after boot when you sign in.

## Use the menu

- Up and Down move.
- Enter opens an item or changes the selected setting.
- `q` goes back. On the home screen, `q` quits.
- A destructive forget or factory reset needs `2` twice.

The menu can show:

- dongle settings;
- headset and controller settings;
- remembered headsets;
- more than one connected device;
- battery from 0 to 100;
- separate left, right, and case batteries;
- installed and available firmware.

Long setting lists scroll. On/off settings show `ON` or `OFF`. Settings with
several choices show the current choice. Jabridge uses the connected device and
its matching public model profile, so unsupported choices stay hidden. If the
profile is unavailable, choice settings stay read-only.

A wired headset with no battery shows only its name. It does not show `0%` or
an unavailable-battery message in the menu.

## Useful commands

| Command | What it does |
| --- | --- |
| `./jabridge status` | Show connected devices |
| `./jabridge battery` | Show battery, or only `Battery: unavailable` |
| `./jabridge diagnose` | Check USB and management-interface discovery |
| `./jabridge settings` | Show supported settings and choices |
| `./jabridge model` | Match the current device profile |
| `./jabridge models` | Summarize Jabra's live model catalog |
| `./jabridge sound` | Show Jabra PipeWire sound devices |
| `./jabridge firmware` | Show installed and available firmware |
| `./jabridge update --check --prerelease` | Check for a newer test build |
| `./jabridge setup` | Repeat the one-time Linux access setup |
| `./jabridge service status` | Check the background service |

Change a setting by copying its name and one of its choices:

```bash
./jabridge settings set dongle.auto-pairing on
./jabridge settings set headset.voice-prompts voice
./jabridge settings set headset.three-dot-button push-to-talk
```

Every successful setting change is read back from the device before Jabridge
says it worked. Bash completion can fill in commands, setting names, and known
values:

```bash
source <(./jabridge completion bash)
```

## Update Jabridge

Check first:

```bash
./jabridge update --check --prerelease
```

Install the newest test build:

```bash
./jabridge update --prerelease
```

Jabridge verifies the release signature and checksum before replacing itself.
This updates only the app. It never updates headset or dongle firmware.

## Firmware

These commands are read-only:

```bash
./jabridge firmware
./jabridge firmware download
./jabridge firmware verify ./firmware/FILE.zip
```

Download saves a file and checks it against Jabra's published release checksum;
it does not install it. Verify checks that the exact official bytes match an
attached device; it does not install it.

If both a headset and dongle are present, the TUI says `Target 1 of N`. Press
Enter to choose the next target. The CLI asks for an exact product ID instead
of guessing, for example:

```bash
./jabridge firmware download --pid 24c7
```

Firmware installation uses Jabridge's native updater. It first checks the
official checksum, attached device, supported payload layout, model, and
version, then asks the user to type `INSTALL`:

```bash
./jabridge firmware install ./firmware/FILE.zip
```

If an update attempt did not finish, run the same command with the exact same
archive again:

```bash
./jabridge firmware install ./firmware/FILE.zip
```

Jabridge recognizes its private unfinished-transfer marker and asks for
`RECOVER`, then replays the complete archive. A different archive is refused.
`--i-accept-risk` skips the typed prompt only for deliberate automation. The
risk comes from a wrong or interrupted transfer command, not from a correct
model-matched firmware file. Changed-PID recovery is not guessed automatically.

The detailed behavior and current evidence are in the
[firmware guide](docs/FIRMWARE.md).

The [device support guide](docs/DEVICE_SUPPORT.md) explains which Jabra
families can be identified now, which operations are tested, and why firmware
and pairing support must be added by protocol family.
The [firmware catalog audit](docs/FIRMWARE_CATALOG_AUDIT.md) records a complete
latest-file download and checksum check without committing vendor firmware.

## Background service and IPC

The service is the hardware owner. The TUI starts it when needed, connects over
a private Unix socket, listens for events, and reconnects after a service
restart.

On systems without a systemd user manager, setup uses desktop-session
autostart instead.

Simple service controls are:

```bash
./jabridge service start
./jabridge service status
./jabridge service restart
./jabridge service stop
```

The easy IPC commands and copy-paste examples are in the
[online IPC guide](https://github.com/Watchdog0x/jabridge/blob/codex/native-go-rewrite/docs/IPC.md).
The same `IPC.md` is available beside each new release archive. Legacy direct
CLI hardware commands temporarily pause the service and start it again when
finished, so two processes never own the device at the same time.

## What is tested

On one Link 380 (`0b0e:24c7`), Jabridge passes device and firmware detection,
model matching, saved-headset reads, reconnect checks, and all five current
setting reads. Controlled tests changed and restored each of these settings:

- auto pairing;
- prioritize computer audio;
- dedicated call mode;
- Bluetooth radio;
- softphone integration.

Real-headset battery, settings, programmable buttons, remembered-device writes,
factory reset, and interrupted-transfer recovery are not release-qualified yet.

## Help test

Use a real headset and follow the short read-only checklist in
[HARDWARE_TESTING.md](HARDWARE_TESTING.md). Post results in
[issue #34](https://github.com/Watchdog0x/jabridge/issues/34). Never post a
serial number or Bluetooth address.

## For developers

Build the static binary with Go 1.23.2 or newer:

```bash
make check
make build
```

The result is `dist/bin/jabridge`. Read [CONTRIBUTING.md](CONTRIBUTING.md) and
[SECURITY.md](SECURITY.md) before changing hardware-write code.

## Independent project

Jabridge is an independent community project. It is not made, approved, or
supported by GN Audio A/S. Jabra is a trademark of GN Audio A/S. Product names
are used only to describe compatibility.

The source is licensed under [Apache-2.0](LICENSE). Third-party tools,
libraries, and firmware are not redistributed here.

## Keywords

- Jabra Direct Linux
- Jabra headset Linux support
- Jabra Linux command-line tool
- Manage Jabra devices on Linux
- Jabra Link 380 Linux
- Jabra Evolve2 85 Linux
