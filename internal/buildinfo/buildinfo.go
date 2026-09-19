package buildinfo

const (
	Name        = "Jabridge"
	ServiceName = "jabridge"
)

// Version can be overridden by release builds with -ldflags -X.
var Version = "1.1.1-test.1"
