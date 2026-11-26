// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Kobayashi-classmate/Cairn/catalog"
)

// originsFile records which source each line was installed from: a line can
// come from several (the system repositories, a vendor repository), often
// under the same package names, so the installed packages alone do not
// tell. Lines installed before the record existed, or outside the panel,
// are found by trying their sources in the default order.
const originsFile = "/var/lib/cairn-helper/software/origins.json"

// origins maps software -> line -> origin.
type origins map[string]map[string]string

func (s *Software) originsPath() string { return s.path(originsFile) }

// readOrigins returns the record; an unreadable one counts as empty (the
// sources are tried in order then).
func (s *Software) readOrigins() origins {
	s.originsMu.Lock()
	defer s.originsMu.Unlock()
	return s.readOriginsLocked()
}

func (s *Software) readOriginsLocked() origins {
	out := origins{}
	data, err := os.ReadFile(s.originsPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.Log.Warn("software origins unreadable", "err", err)
		}
		return out
	}
	if err := json.Unmarshal(data, &out); err != nil {
		s.Log.Warn("software origins unreadable", "err", err)
		return origins{}
	}
	return out
}

// recordedOrigin is the origin recorded for a line, "" when none is.
func (s *Software) recordedOrigin(software, line string) string {
	return s.readOrigins()[software][line]
}

// setOrigin records where a line came from; origin "" drops the line.
func (s *Software) setOrigin(software, line, origin string) error {
	s.originsMu.Lock()
	defer s.originsMu.Unlock()
	all := s.readOriginsLocked()
	if origin == "" {
		if all[software][line] == "" {
			return nil
		}
		delete(all[software], line)
		if len(all[software]) == 0 {
			delete(all, software)
		}
	} else {
		if all[software] == nil {
			all[software] = map[string]string{}
		}
		all[software][line] = origin
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.originsPath()), 0o700); err != nil {
		return err
	}
	return writeFileAs(s.originsPath(), append(data, '\n'), 0o600)
}

// lineOffer resolves a line of sw from the source with origin ("" for the
// default one); false when it is not offered from there on this system or
// this helper cannot install from that kind of source.
func (s *Software) lineOffer(sw *catalog.Software, line, origin string) (catalog.Offer, bool) {
	o, ok := sw.OfferFrom(s.system(), line, origin)
	if !ok || !s.handles(o.Method) {
		return catalog.Offer{}, false
	}
	return o, true
}

// currentOffer is a line as it is here: from the origin it was installed
// from when that is recorded and still offered, else from its default
// source. Sites and databases go by it to find a line's files.
func (s *Software) currentOffer(sw *catalog.Software, line string) (catalog.Offer, bool) {
	if origin := s.recordedOrigin(sw.ID, line); origin != "" {
		if o, ok := s.lineOffer(sw, line, origin); ok {
			return o, true
		}
	}
	return s.lineOffer(sw, line, "")
}
