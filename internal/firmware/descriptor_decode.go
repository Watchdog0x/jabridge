package firmware

// DecodeHIDDescriptor parses supplied descriptor bytes without opening a device.
func DecodeHIDDescriptor(data []byte) ([]HIDReport, error) { return parseHIDReports(data) }
