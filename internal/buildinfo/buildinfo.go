package buildinfo

const (
	Name        = "Jabridge"
	ServiceName = "jabridge"
)

// Version can be overridden by release builds with -ldflags -X.
var Version = "1.5.0-preview.3"

// SelfUpdate can be overridden with -ldflags -X by distribution packagers.
// Only "enabled" permits app updates; other values disable them.
var SelfUpdate = "enabled"

// SelfUpdateEnabled reports whether this build permits app self-updates.
func SelfUpdateEnabled() bool {
	return SelfUpdate == "enabled"
}

// Setup can be overridden with -ldflags -X by distribution packagers. Only
// "enabled" permits Jabridge to install its own device access rules, user
// service, binary copy and completion; other values disable that.
var Setup = "enabled"

// SetupEnabled reports whether this build may install its own setup files.
func SetupEnabled() bool {
	return Setup == "enabled"
}
