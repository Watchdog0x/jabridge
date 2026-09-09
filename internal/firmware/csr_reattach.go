package firmware

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func verifyCSRReattach(original USBDevice, address byte, wanted string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		// A still-present original attachment is not evidence of a reboot.
		if validateUSBDevice(original) != nil {
			devices, err := enumerateBoundUSB()
			if err != nil {
				return err
			}
			for _, device := range devices {
				if device.SysPath != original.SysPath || device.VendorID != original.VendorID || device.ProductID != original.ProductID {
					continue
				}
				if original.Serial != "" && device.Serial != original.Serial {
					return errors.New("reconnected firmware target has a different serial identity")
				}
				transport, err := openBoundManagement(device)
				if err != nil {
					continue
				}
				remaining, _ := ctx.Deadline()
				version, err := QueryFirmwareVersion(transport, address, 1, min(time.Second, time.Until(remaining)))
				_ = transport.Close()
				if err != nil {
					continue
				}
				if version != wanted {
					return fmt.Errorf("installed firmware reads %s, expected %s", version, wanted)
				}
				fmt.Printf("Firmware %s installed and read back from the selected USB port.\n", version)
				return nil
			}
		}
		if err := waitDFU(ctx, 100*time.Millisecond); err != nil {
			return fmt.Errorf("selected device did not reboot and return firmware %s: %w", wanted, err)
		}
	}
}
