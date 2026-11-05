# Engage 75 component check

The requested report has now been returned for issue #62. Do not repeat this
check. The instructions below document that diagnostic build. The newer
compatibility executor is disabled and has no approved hardware trial yet.

This candidate checks another way to read the missing firmware image IDs for
issue #62. It only sends information requests. It does not ask the headset to
restart or update. **The firmware update is still not fixed.** Keep the headset
docked and use the same base. Run this check once.

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
  ./jabridge debug --buttons=false --dect-image-info --output jabridge-engage75-image-info.txt
```

The report should say the service matches the app version and contain
**DECT runtime image information**. A NOT TESTED message
asking for the candidate service means the client reached an older service.
The report does not include serial numbers or raw device packets.

The headset-presence line uses the version reply already read by this check.
An answering headset can still reject the identity checks needed for an update.
The report names those blockers; it does not treat them as proof of undocking
or skip them. Docking advice appears only when no headset query succeeded.

The extra check reads the base, headset and display once each. A rejected,
timed-out or unrecognized reply is useful information too. Image IDs are not
the same thing as runtime USB IDs. Even a matching result is not permission to
try firmware installation with this candidate. Do not repeat the test unless
asked.

When finished, press Ctrl-C in the candidate service terminal, then restart the
installed service using its normal method:

```sh
systemctl --user start jabridge.service
```

Attach only `jabridge-engage75-image-info.txt` to #62. No screenshots, repeat
runs or undocked test are needed.
