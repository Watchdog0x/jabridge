# Using Jabridge

On Link 390 `2e56` or `2e57`, variant `04-10`, firmware `3.2.0`, Dongle settings
can offer Preferred connection mode and Computer audio. These require matching
device and model data and have software tests only. They are separate from
Prioritize computer audio and do not enable extra pairing or reset actions.

For downloads and setup, see the [quick start](../README.md#start).

## Navigation

| Key | Action |
| --- | --- |
| `w` or `↑` | Move up |
| `s` or `↓` | Move down |
| `Enter` | Select an option |

### Side Menu

| Key | Action |
| --- | --- |
| `1`, `2`, `3`, `4` | Select the option shown on screen |
| `Enter` | Open or confirm the selected option |
| `q` | Go back |

Long lists scroll as you move. Choose Quit from the main menu to close the app.

With a Link 380 or Link 390, choose **Find headset** and put your headset in pairing mode. Q stops the search.

## App updates

When you open Jabridge in a terminal, it checks for a new app version. If one
is available, the menu shows an update screen. Press `Y` to update or `N` to
continue. You can also use the arrow keys and Enter; No is selected by default.


The command line asks:

```text
A new Jabridge update is available: VERSION
Update now? (yes/no) [no]:
```

Type `yes` to update and restart, or `no` to continue. Enter also means no.
The prompt appears before the menu or command starts. If the check fails or
takes more than two seconds, Jabridge continues normally. Scripts and
background services do not get the prompt.

You can also update directly:

```bash
./jabridge update
```

This updates the app, not your device firmware.

## Noise control on Evolve2 85

Switch between ANC, HearThrough and Off:

```bash
jabridge settings set headset.noise-control anc
jabridge settings set headset.noise-control hearthrough
jabridge settings set headset.noise-control off
```

These modes were tested on Evolve2 85 through Link 380 in
[PR #48](https://github.com/Watchdog0x/jabridge/pull/48). Direct USB is untested.
The mixed HearThrough mode and HearThrough level are not offered.

## Headset volume (experimental)

For supported Evolve2 30 SE models with firmware 1.11.0, connected directly by USB:

```sh
jabridge headset volume 50
```

Start audio playback, then use this command to change the headset’s own volume.
The headset rounds requests to its internal steps. At a volume limit, it may
also send a normal volume key to Linux.
`jabridge sound volume` controls Linux volume separately.

Check support for your selected model and connection:

```sh
jabridge headset volume info
```

Read the saved headset level:

```sh
jabridge headset volume get
```

The saved level can differ from the current playback volume. Running
`jabridge headset volume` without a value also reads the saved level.

Volume writes and saved-level reads have been tested on the Evolve2 30 SE
(0b0e:0e36) from [issue #44](https://github.com/Watchdog0x/jabridge/issues/44).
The four USB variants (0e36, 0e37, 0e38 and 0e39) have model-specific checks tested
against their original firmware. The other three variants still need physical
testing. Saved readings can stay unchanged for several seconds after a volume
request and do not verify current playback. A headset-volume command through a Link
dongle is still being investigated; it is not enabled yet.

## Commands

See all commands:

```bash
./jabridge --help
```

| Command | What it does |
| --- | --- |
| `./jabridge status` | Show connected devices |
| `./jabridge battery` | Show battery level |
| `./jabridge settings` | View or change settings |
| `./jabridge sound` | Control volume and microphone |
| `./jabridge firmware` | Check device firmware |
| `./jabridge update` | Update Jabridge |
| `./jabridge service status` | Check the background service |

## Problems

### Silent playback on Evolve2 30 SE

If audio is playing but you hear nothing, this experimental command tries the
recovery sequence reported in [issue #44](https://github.com/Watchdog0x/jabridge/issues/44).
It currently supports Evolve2 30 SE connected directly by USB.

Stop calls and recordings first, keep audio playback running, then run:

```sh
jabridge sound recover
```

This briefly opens the headset microphone and discards the captured audio while
restarting playback. It leaves volume, mute and the selected audio profile alone.
Check whether you can hear sound afterward. Audible recovery has not been
confirmed. This command does not fix the separate Linux kernel mixer problem.

### Debug report

Before opening an issue, save a debug report:

```bash
./jabridge debug --output jabridge-debug.txt
```

If that file already exists, Jabridge saves a new report as `jabridge-debug-1.txt`, then `jabridge-debug-2.txt`, and so on. The command shows the exact file to attach.

Check that file before sharing it. Then [open an issue](https://github.com/Watchdog0x/jabridge/issues/new), attach the file and tell us your device model and what went wrong. Debug does not change settings or firmware.
