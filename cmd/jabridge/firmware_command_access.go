package main

// File inspection, help and unknown commands must not interrupt the service.
func firmwareCommandNeedsHardware(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		switch args[0] {
		case "download", "verify", "install", "manifest":
			return false
		}
	}
	switch args[0] {
	case "status", "list", "info", "check", "download", "verify", "install":
		return true
	default:
		return false
	}
}
