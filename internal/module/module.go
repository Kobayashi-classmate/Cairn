// SPDX-License-Identifier: AGPL-3.0-only

// Package module defines the contract every feature module implements. Each
// module registers its own routes explicitly; modules talk to each other only
// through public interfaces, never through each other's internals.
//
// The interface grows with the platform: permissions, helper actions,
// migrations, jobs, events, locales and AI tools are added as the P0
// subsystems that need them land.
package module

import (
	"github.com/Kobayashi-classmate/Cairn/internal/api"
)

// Module is a feature module of the panel.
type Module interface {
	// ID is a stable, lower-case identifier such as "core" or "system".
	ID() string
	// Routes registers the module's HTTP endpoints.
	Routes(r *api.Router)
}
