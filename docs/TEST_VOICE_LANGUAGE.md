# Voice language preview

This update path has software tests. It has not changed a real headset yet.
Testing is optional. Keep the original official firmware ZIP and recovery record
if it stops. The headset may need recovery if an update fails.
File checks use the official catalog checksum and a saved SHA256 of the archive.

Use the separate review build. Stop its menu and any manually started review
daemon before the command below. Keep the Evolve3 65 connected directly by USB.
This preview supports USB ID 0b0e:2549, variant 01-A3, with main firmware 1.0.1.

List languages without changing a device:

```bash
./jabridge firmware languages firmware/Jabra_Evolve3_65_v1.0.1_2549_vector_Duo.zip
```

Change to German:

```bash
JABRIDGE_ENABLE_CSR17_LANGUAGE=I_ACCEPT_UNTESTED_LANGUAGE_UPDATE \
  ./jabridge firmware install \
  firmware/Jabra_Evolve3_65_v1.0.1_2549_vector_Duo.zip --language German
```

The command checks the device and asks you to type INSTALL. It restarts the
headset and reads back the language and voice-pack version. Please check that
the spoken prompts are German, Jabra Plus shows German, and sound, microphone
and reconnecting still work. Then repeat with `--language English` and check again.

The menu also offers **Firmware → L Voice language** when started with the same
preview opt-in. Setup and app updates remain disabled in the review binary.
Use the isolated service instructions from TEST_LINK390.md for the menu.

If a commit is unconfirmed, reconnect USB and repeat the exact language command
with the same ZIP. It asks for RECOVER and only reads the headset. Do not delete
the recovery record or start a different firmware update.
If the headset is confirmed unchanged after reconnecting, RECOVER clears the
record and says the update did not apply. You can then deliberately start a new
update. A state matching neither the old nor requested language stays blocked.
