//go:build !console

package consolefs

import "io/fs"

// Files reports that no console is embedded in this build.
func Files() fs.FS { return nil }
