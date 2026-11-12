# Engage 75 firmware test

Download the current test ZIP linked in issue #62 and extract it into a folder.
Open a terminal in that folder and run:

```sh
bash test-engage75.sh
```

Keep the headset docked and base plugged in. End calls and close other Jabra
tools. The device restarts and reconnects during a firmware update.

For a first test, press Enter at the first question. For a new attempt after
the earlier reconnect failure, remove base power for a few minutes, reconnect
it and wait for its normal screen. Run the new helper and type `NEW ATTEMPT`
at the first question. It checks the saved original device and firmware state;
it does not continue an interrupted transfer or delete the old record.

Type `UPDATE` when asked to start. The helper downloads and checks the correct
5.20.1 firmware. Your installed app is not replaced. It keeps the previous
result and prints the new result filename to attach to issue #62.

If it stops, attach the result before trying again. Recovery after a partially
written firmware update has not been tested; the earlier power-cycle result
only covers the initial reconnect check. No old diagnostic repeat is needed.
