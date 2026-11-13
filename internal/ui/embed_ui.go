// SPDX-License-Identifier: AGPL-3.0-only

//go:build embedui

package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func distFS() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
