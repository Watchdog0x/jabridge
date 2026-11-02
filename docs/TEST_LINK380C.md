# Link 380c pairing

For Link 380c USB ID `0b0e:24c9`, Jabridge 1.6.1 enables Find headset and fresh
pairing without a test option. An Evolve2 85 user [reported successful pairing](https://github.com/Watchdog0x/jabridge/issues/58#issuecomment-6573380991)
with the earlier 1.5.0 test build. This is one setup, not a test of every headset.

Remembered-device connect, disconnect and forget, dongle settings writes and
factory reset remain disabled for this USB ID. Other Link 380 variants have
not been added by this change. The old service-start option is no longer used.

## Pair a headset

Update through your package manager, or for official downloads run:

```sh
jabridge update
```

Close other device-control apps and open the menu:

```sh
jabridge
```

Choose **Find headset**. On Evolve2 85, hold the On/Off/Pairing switch in the
pairing position for 3 seconds until the light flashes blue and the headset
announces pairing mode. See [Jabra's instructions](https://www.jabra.com/en-sg/supportpages/jabra-evolve2-85).
Select the headset and connect. Do not reset it again.

The menu uses the running service's pairing permission. If an older service
still has pairing disabled, update and restart that service too. For an official
installation:

```sh
jabridge service restart
```

Package-managed installations use their package's service instructions.

## Report a problem

Say whether the headset appears and connects. If you also check reconnecting
after turning it off and on, report that result separately. A pairing reply
alone does not prove a working audio connection or reconnect behaviour.

No new log is needed if it works. If something fails, copy the on-screen error
and save the latest report from the same service:

```sh
jabridge debug --output jabridge-link380c.txt
```

This procedure does not test headset settings, firmware updates or factory reset.
