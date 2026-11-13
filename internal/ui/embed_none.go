// SPDX-License-Identifier: AGPL-3.0-only

//go:build !embedui

package ui

import "io/fs"

func distFS() (fs.FS, bool) { return nil, false }
