package main

func drawPairingRows(devices []pairedDevice) {
	_, _, bottom := panelBounds()
	start, end := listWindow(currentSelection, len(devices), max(1, bottom-8))
	drawListWindowHint(7, start, end, len(devices))
	for i := start; i < end; i++ {
		state := "Remembered"
		if menuState == screenSearch {
			state = "Available"
		}
		if devices[i].isConnected {
			state = "Connected"
		}
		drawLabelValue(8+i-start, devices[i].deviceName, state, i == currentSelection)
	}
}
