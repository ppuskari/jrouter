package meta

import (
	_ "embed"
	"strings"
)

// Name of the software.
const Name = "jrouter"

//go:embed VERSION
var rawVersion string

// Version is the SemVer version string (without 'v' prefix).
var Version = strings.TrimSpace(rawVersion)

// NameVersion is the full name and version string.
var NameVersion = Name + " v" + Version
