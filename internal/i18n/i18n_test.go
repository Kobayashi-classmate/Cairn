// SPDX-License-Identifier: AGPL-3.0-only

package i18n

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"en": "en", "en-US": "en", "en_GB.UTF-8": "en",
		"zh": "zh-CN", "zh-CN": "zh-CN", "zh_CN.UTF-8": "zh-CN", "zh-Hans": "zh-CN",
	}
	for in, want := range cases {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "C", "POSIX", "C.UTF-8", "!!"} {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) = %q, want no match", in, got)
		}
	}
}

func TestNegotiate(t *testing.T) {
	if got := Negotiate("zh-CN,zh;q=0.9,en;q=0.8", "en"); got != "zh-CN" {
		t.Errorf("got %s", got)
	}
	if got := Negotiate("fr-FR", "zh-CN"); got != "zh-CN" {
		t.Errorf("fallback not used: %s", got)
	}
	if got := Negotiate("", "xx"); got != Default {
		t.Errorf("default not used: %s", got)
	}
}

var placeholder = regexp.MustCompile(`\{\{\s*\.(\w+)\s*\}\}`)

// TestLocaleParity ensures every locale has exactly the keys of the English
// source file and the same template placeholders for each key.
func TestLocaleParity(t *testing.T) {
	load := func(tag string) map[string]string {
		data, err := localeFS.ReadFile("locales/" + tag + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := toml.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		return m
	}
	src := load(Default)
	for _, tag := range Supported {
		if tag == Default {
			continue
		}
		dst := load(tag)
		for k, v := range src {
			tv, ok := dst[k]
			if !ok {
				t.Errorf("%s: missing key %s", tag, k)
				continue
			}
			if a, b := names(v), names(tv); a != b {
				t.Errorf("%s: key %s placeholders %s, English has %s", tag, k, b, a)
			}
		}
		for k := range dst {
			if _, ok := src[k]; !ok {
				t.Errorf("%s: key %s does not exist in %s", tag, k, Default)
			}
		}
	}
}

// names returns the sorted placeholder names used in a message.
func names(s string) string {
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestCatalogRendersAndFallsBack(t *testing.T) {
	c, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	en := c.T("en", "cli.reset_password.no_user", map[string]any{"Username": "bob"})
	if en != "There is no user named bob." {
		t.Errorf("en render: %q", en)
	}
	// The translation itself lives only in the locale file (program files stay
	// English); check that it is used and interpolated.
	if zh := c.T("zh-CN", "cli.reset_password.no_user", map[string]any{"Username": "bob"}); zh == en || !strings.Contains(zh, "bob") {
		t.Errorf("zh-CN render: %q", zh)
	}
	if got := c.T("fr", "cli.reset_password.no_user", map[string]any{"Username": "bob"}); got != en {
		t.Errorf("fallback render: %q", got)
	}
	if got := c.T("en", "no.such.key", nil); got != "no.such.key" {
		t.Errorf("unknown id should be returned as-is, got %q", got)
	}
}
