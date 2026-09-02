//go:build console

package consolefs

import (
	"embed"
	"io/fs"
)

// The Next.js export, copied here by `npm run build:embed` in web/console.
//
//go:embed all:out
var exported embed.FS

// Files returns the exported console rooted at its index.
func Files() fs.FS {
	sub, err := fs.Sub(exported, "out")
	if err != nil {
		return nil
	}
	return sub
}
