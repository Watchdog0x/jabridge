# Testing your device

## Tested devices

Testing a device does not mean every feature or firmware update has been tested.

| Device |
| --- |
| Jabra Link 380 |
| Jabra Evolve2 65 |
| Jabra Evolve2 85 |
| Jabra Evolve3 85 |
| Jabra Speak 510 |
| Jabra Engage 50 II and Link Call Control |

Tested another device? [Open an issue](https://github.com/Watchdog0x/jabridge/issues/new)
with its name, what worked and your debug report so we can add it to this list.

## How to test

Use your device normally. Try sound, microphone, buttons and settings. Then save a report:

```bash
./jabridge debug --output jabridge-debug.txt
```

Check the report before sharing it. [Open an issue](https://github.com/Watchdog0x/jabridge/issues/new), attach the file and tell us your device model, how it is connected and what worked or failed.

You do not need to update firmware or reset your device to test it.

## Firmware recovery

1. Keep everything plugged in. Retry with the **same firmware file** (replace `FILE.zip`):

   ```bash
   ./jabridge firmware install ./firmware/FILE.zip
   ```

2. Type `RECOVER` when asked. Leave everything plugged in until it finishes. If it fails or recovery is unavailable, stop and [send a debug report](#how-to-test).
