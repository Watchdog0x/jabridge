# Engage 75 component check

This candidate adds a read-only check for issue #62. It reads the base,
headset, display and radio separately. It does not write firmware or settings,
change modes, enter a bootloader, or start a transfer. The update failure is
still being investigated. Keep the headset docked and hold off on Jabra Direct
if you can keep this setup available for the test.

Download the reviewed candidate for your CPU into a separate folder. Compare
its SHA-256 with the value in the test-build comment:

```sh
sha256sum ./jabridge
chmod +x ./jabridge
./jabridge --version
```

Do not run setup, update, or firmware install from this candidate. Keep your
installed app. Close other Jabra tools and end calls before checking.

Stop the installed service. Package-managed users should use their package's
service instructions; for the normal user service:

```sh
systemctl --user stop jabridge.service
```

In the candidate folder, start its service in a foreground terminal:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-engage75-check.sock" ./jabridge --daemon
```

Leave that terminal open. In a second terminal in the same folder, run:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-engage75-check.sock" \
  ./jabridge debug --buttons=false --output jabridge-engage75-components.txt
```

The report should say the service matches the app version and contain
**DECT components (read-only, performed by the service)**. A NOT TESTED message
asking for the candidate service means the client reached an older service.
The report does not include serial numbers or raw device packets.

The check makes one attempt; if it passes but the update still fails, run it
twice more and attach all three files. A passing identity attempt is not
permission to try firmware installation with this candidate.

When finished, press Ctrl-C in the candidate service terminal, then restart the
installed service using its normal method:

```sh
systemctl --user start jabridge.service
```

Attach `jabridge-engage75-components.txt` to #62. No screenshots are needed.
Optionally, repeat the read-only check once with the headset undocked and give
that output a different name. Label it **undocked**. This second check is
optional; dock the headset again afterward.
