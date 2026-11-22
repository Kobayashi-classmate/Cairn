// SPDX-License-Identifier: AGPL-3.0-only

package sites

import (
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func TestDeriveCode(t *testing.T) {
	for domain, want := range map[string]string{
		"www.example.com":                     "example-com",
		"*.shop.example.org":                  "shop-example-org",
		"1.example.com":                       "site-1-example-com",
		"a-very-long-domain-name.example.com": "a-very-long-domain-n",
		// cw-<code>-run would be another site's runner.
		"site.run":     "site-rn",
		"www.blog.run": "blog-rn",
	} {
		got := deriveCode(domain)
		if got != want {
			t.Errorf("deriveCode(%q) = %q, want %q", domain, got, want)
		}
		if !helperapi.ValidSiteCode(got) {
			t.Errorf("deriveCode(%q) = %q is not a valid code", domain, got)
		}
	}
}
