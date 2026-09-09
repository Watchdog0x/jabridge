# Jabridge: Jabra Direct for Linux

Jabridge, formerly jLink, brings Jabra headset and dongle management to Linux.
Think of it as a community alternative to Jabra Direct, with a simple terminal
menu, command line tools and a background service.

## We are building the new Jabridge

Jabridge 1.0.0 is a native Go rewrite. No Jabra library, .NET or Node.js is
needed to run it. Download the compiled app and get started.

We are getting close to the first stable release.

The new code is on the [native rewrite branch](https://github.com/Watchdog0x/jabridge/tree/codex/native-go-rewrite).
The source on `main` is still the old version while the rewrite is being tested.

## Try the new version

Download the newest **1.0.0 preview** from [Releases](https://github.com/Watchdog0x/jabridge/releases),
extract it and open a terminal in that folder:

```bash
./jabridge setup
```

Run setup first and follow the prompts. Then open the menu:

```bash
./jabridge
```

Jabridge opens the menu and helps you set up device access and the background
service. Approve the password prompt if asked. Run the app as your normal
user, not with `sudo`.

Update Jabridge with:

```bash
./jabridge update
```

## What can it do?

Manage supported headset and dongle settings, see battery levels, switch
devices, view remembered headsets and check firmware. Direct USB headsets
can work without a dongle. Available settings depend on your model and the
native commands implemented so far.

The menu uses the background service through IPC. You can use the same
JSON-RPC API to build your own desktop app, panel widget, scripts or other
tools. Read device information, change supported settings and receive device
events without writing your own USB driver.

Start with the [easy IPC guide](https://github.com/Watchdog0x/jabridge/blob/codex/native-go-rewrite/docs/IPC.md).
Firmware installation temporarily pauses the service for exclusive device
access; it is not a remote IPC flashing API.

## Firmware updates

You can download and install firmware with Jabridge's native updater on
supported models and update protocols. Firmware checks and downloads are
separate from installation:

```bash
./jabridge firmware
./jabridge firmware download
```

Use the exact file name printed by the download command:

```bash
./jabridge firmware install ./firmware/FILE.zip
```

The installer checks compatibility and asks you to type `INSTALL` before
writing. Keep the device plugged in and do not update during a call.

Not every model has been tested. Successful updates on one device do not
prove that another model or interrupted update recovery works. If an update
fails, keep the same file and USB port and share the error before retrying.
Read the [firmware guide](https://github.com/Watchdog0x/jabridge/blob/codex/native-go-rewrite/docs/FIRMWARE.md)
for supported methods and recovery limits.

## Problems?

Before opening an issue, save a debug report:

```bash
./jabridge debug --output jabridge-debug.txt
```

Check `jabridge-debug.txt` before sharing it. Then
[open an issue](https://github.com/Watchdog0x/jabridge/issues/new), attach the
file and tell us your device model, connection and what went wrong.
Debug does not change settings or firmware.

## Independent project

Jabridge is a community project, not an official Jabra product. It is not
made, approved or supported by GN Audio A/S. Jabra is a trademark of GN Audio A/S.
The source is licensed under [Apache 2.0](LICENSE).

## Keywords

Jabra Direct Linux  
Jabra headset Linux support  
Jabra Linux command-line tool  
Manage Jabra devices on Linux  
Jabra Link 380 Linux  
Jabra Evolve2 85 Linux
