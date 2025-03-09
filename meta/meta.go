package meta

const (
	// Name of the software.
	Name = "jrouter"

	// Version is the SemVer version string (without 'v' prefix or any suffixes).
	Version = "0.0.13"
)

// Suffix is the SemVer suffix. It's usually set with the Go compiler
// flag:
//
//	-ldflags "-X gitea.drjosh.dev/josh/jrouter/meta.Suffix=${VERSION_SUFFIX}"
var Suffix = "-dev"

// NameVersion is the full name and version string.
var NameVersion = Name + " v" + Version + Suffix
