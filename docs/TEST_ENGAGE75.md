# Engage 75 firmware test

Use the **Engage 75 test download linked in issue #62**. Extract the ZIP, then
open a terminal in the extracted folder and run:

```sh
bash test-engage75.sh
```

Keep the headset docked and the base plugged in. End calls and close other
Jabra tools. The script gets the correct firmware, checks the files and saves
one result file. It asks you to type `UPDATE` before starting.

This test can install firmware **5.20.1** and restart the device. If it fails,
the base may stay in update mode. A recovery method for firmware 4.4.1 has not
been confirmed. This is a hardware test, not a confirmed fix. Your installed
Jabridge app is not replaced. Do not run setup or app update from this build.

When it finishes or stops, attach **jabridge-engage75-result.txt** to issue #62.
If it stops, leave the base connected and do not run the test again.
No repeat of the old diagnostic check is needed.
