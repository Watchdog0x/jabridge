// Package assets contains the files installed or displayed by Jabridge.
package assets

import _ "embed"

// UdevRule grants access to supported USB and input devices.
//
//go:embed 70-jabridge.rules
var UdevRule []byte

// UserService is the per-user background service template.
//
//go:embed jabridge.service
var UserService []byte

// ThirdPartyNotices contains the licenses shipped with the executable.
//
//go:embed THIRD_PARTY_NOTICES.md
var ThirdPartyNotices string
