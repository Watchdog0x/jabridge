package buildinfo

const (
	Name        = "Jabridge"
	ServiceName = "jabridge"
)

// Version can be overridden by release builds with -ldflags -X.
var Version = "1.2.1"

// SelfUpdate can be overridden with -ldflags -X by distribution packagers.
// Only "enabled" permits app updates; other values disable them.
var SelfUpdate = "enabled"

// SelfUpdateEnabled reports whether this build permits app self-updates.
func SelfUpdateEnabled() bool {
	return SelfUpdate == "enabled"
}
