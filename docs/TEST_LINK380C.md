# Test Link 380c pairing

For Link 380c USB ID `0b0e:24c9`. This optional preview has not paired on that
variant yet. Pairing can change saved pairings. It cannot forget or disconnect
remembered headsets, change dongle settings, or factory reset this dongle.
Other Link 380 variants have not been added by this change.

Download the Linux review artifact linked in PR #55 and extract it into a
separate folder. The review build cannot install itself or update the normal app.

```sh
chmod +x ./jabridge
./jabridge --version
jabridge service stop
```

Close other device-control apps. In that folder, start the test service:

```sh
JABRIDGE_ENABLE_LINK380C_SEARCH_CONNECT=I_ACCEPT_UNTESTED_PAIRING \
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-link380c.sock" ./jabridge --daemon
```

In a second terminal in the same folder, open the test menu:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-link380c.sock" ./jabridge
```

The option belongs on the service, not just the menu command. Without it,
search and the remembered list stay read-only. The global experimental-writes
option does not enable this pairing path.

On Evolve2 85, hold the On/Off/Pairing switch in the pairing position for
3 seconds until the light flashes blue and the headset announces pairing mode.
See [Jabra's instructions](https://www.jabra.com/en-sg/supportpages/jabra-evolve2-85).
Choose **Find headset**, select the headset and connect. Do not reset it again.

Please report whether the headset appears, connects, and reconnects after
turning the headset off and on. An acknowledgement by itself is not a successful
connection. No new log is needed if those checks work. If something fails,
copy the on-screen error and save one latest report from the same test service:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-link380c.sock" \
  ./jabridge debug --output jabridge-link380c.txt
```

Finish by closing the test menu and pressing Ctrl+C in its service terminal.
Then start the normal service again:

```sh
jabridge service start
```

These tests do not qualify headset settings, firmware updates or factory reset.
