package web

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var staticFiles embed.FS

// StaticFS returns the embedded static web assets rooted at static/.
func StaticFS() fs.FS {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
