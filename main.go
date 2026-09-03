package main

/*
#cgo CFLAGS: -Iheaders
#cgo LDFLAGS: -L${SRCDIR}/lib -Wl,-rpath,${SRCDIR}/lib -ljabra -l:libcurl.so.4

#include "Common.h"
#include "GoWrapper.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"log"
	"os"
	"unsafe"
)

// sudo apt install libasound2 libcurl4
func main() {

	oldSettings, err := enableRawMode()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to enable raw mode:", err)
		return
	}
	defer restoreTerminal(oldSettings)
	go startKeysPressedListener()

	appId := C.CString("JabraLink")
	C.Jabra_SetAppID(appId)
	defer C.free(unsafe.Pointer(appId))

	// Callback parameters: FirstScanForDevicesDoneFunc, DeviceAttachedFunc, DeviceRemovedFunc,
	// ButtonInDataRawHidFunc, ButtonInDataTranslatedFunc, nonJabraDeviceDetection, configParams
	if init := C.Jabra_InitializeV2(
		(*[0]byte)(C.firstScanForDevicesDone), // Callback when USB scan is complete
		(*[0]byte)(C.deviceAttachedFunc),       // Callback for when a device is attached
		(*[0]byte)(C.deviceRemovedFunc),        // Callback for when a device is removed
		nil,                                    // Callback for raw HID button input (not used here)
		nil,                                    // Callback for translated button input (not used here)
		false,                                  // nonJabraDeviceDetection (not used here)
		nil,                                    // Additional configuration parameters (not used here)
	); !init {
		log.Fatalln("Failed to initialize Jabra SDK")
	}
	defer uninitialize()

	// The current callback behavior is inconsistent. While the charging status updates as expected,
	// the `levelInPercent` callback is sometimes delayed. This causes issues with timely updates.
	// We need to ensure that the callback is triggered in a more predictable and consistent manner.
	// C.Jabra_RegisterBatteryStatusUpdateCallbackV2((*[0]byte)(unsafe.Pointer(C.batteryStatusUpdate)))
	defer close(stopUpdateBattery)
	defer close(stopUpdatePairingList)

	fmt.Print("\x1b[?25l")       // Hide cursor
	defer fmt.Print("\x1b[?25h") // Show cursor again
	clearScreen()
	startUi()

	fmt.Println("\n\nThank you for using jlink! (ʘ‿ʘ)╯")

}
