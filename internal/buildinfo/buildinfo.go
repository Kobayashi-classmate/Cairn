// SPDX-License-Identifier: AGPL-3.0-only

// Package buildinfo exposes version information injected at link time.
package buildinfo

// Version is the release version. It is overridden at build time with
// -ldflags "-X github.com/Kobayashi-classmate/Cairn/internal/buildinfo.Version=1.2.3".
var Version = "0.0.0-dev"
