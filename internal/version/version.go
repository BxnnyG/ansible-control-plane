// Package version holds build metadata.
package version

// Attribution is the product attribution shown in the UI and version output.
const Attribution = "Created by bxnny"

// Version and Commit are set at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
)
