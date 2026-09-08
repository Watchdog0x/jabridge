# Headset and controller support

An Engage 50 II Link is the wired control box with buttons and a volume wheel.
The headset and controller can share one USB connection but answer different
management addresses.

The new code reads their identities separately. It uses the headset identity
to choose the matching settings profile. A missing headset is not replaced by
the controller, and a missing controller is not replaced by the headset.

## Using it

Run `./jabridge`. With both parts detected, the menu shows Headset settings
and Controller settings. With only a headset, only Headset settings appear.
If only the controller answers, its menu stays available and the app explains
that the headset was not detected. Settings owned by the headset cannot work
until it answers. Without its exact model profile, controller reads remain
limited and writes are not enabled.

`./jabridge settings` lists both groups. Existing `headset.SETTING` commands
still work. Controller settings also accept `controller.SETTING`, for example:

```bash
./jabridge settings set controller.controller-name "Work controller"
```

Only settings that were read successfully are offered. A menu group does not
decide the hardware address. Some controller button settings are stored through
the main headset address. Controller name and ringer settings use a separate
address.

If a part changes while you edit, reopen its settings before saving. A setting
write and readback check the selected part's identity. The service checks for
part changes while the USB setup remains connected. Unconfirmed reconnects
after a setting-triggered restart are reported as a failure, not assumed to be
the same device.

## Test report

```bash
./jabridge debug --output controller-report.txt
```

Attach the report to [issue 5](https://github.com/Watchdog0x/jabridge/issues/5).
Use another filename when repeating without the controller.

The report shows each part's identity result and each setting's command route.
Private device identities and typed names are not included. Debug does not
change settings or firmware.

## What is covered

The initial part rules cover Engage 50 II USB IDs 4051 through 4056. Combined
headset/controller variants use addresses 1 and 3; headset-only variants use
address 1. Every role still requires a real identity reply. Other models keep
their existing routing, including Link 380 and headsets connected through it.

This is a controller test build. Automated identity, routing, binding and menu
tests do not replace a physical Engage test. Button behavior, audio, setting
persistence after unplugging and firmware installation are separate tests.
This change does not add a firmware installer for Engage devices.

Controller-only USB identification still needs a real report. If that mode
uses a different USB ID, it needs its own model rule. Other products are not
automatically covered by the Engage rules; each needs verified identities,
command routes and hardware tests.

Model rules follow the public model catalog and the address overrides in
[Jabra's property definitions](https://www.npmjs.com/package/@gnaudio/jabra-properties-definition).
[Jabra's properties guide](https://developer.jabra.com/sdks-and-tools/javascript/properties)
explains the property interface. No vendor runtime is needed by Jabridge.
