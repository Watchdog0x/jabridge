// Package completion embeds shell completion scripts for the command-line tools.
package completion

import _ "embed"

var (
	// JabridgeBash completes the main jabridge command in Bash.
	//go:embed jabridge.bash
	JabridgeBash string

	// JAFWBash completes the jafw command in Bash.
	//go:embed jafw.bash
	JAFWBash string
)
