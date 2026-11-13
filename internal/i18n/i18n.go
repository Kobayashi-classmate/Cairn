// SPDX-License-Identifier: AGPL-3.0-only

// Package i18n holds the list of supported languages, locale negotiation and
// the backend message catalog (CLI output, notifications). English is the
// source language and the fallback for any missing translation. The web UI
// has its own catalog; the backend only returns error codes to it.
package i18n

import (
	"embed"
	"fmt"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/language"
)

// Default is the source and fallback language.
const Default = "en"

// Supported lists the available languages as BCP 47 tags. Adding a language
// means adding locale files and an entry here; no other code changes.
var Supported = []string{"en", "zh-CN"}

var supportedTags = func() []language.Tag {
	tags := make([]language.Tag, len(Supported))
	for i, s := range Supported {
		tags[i] = language.MustParse(s)
	}
	return tags
}()

var matcher = language.NewMatcher(supportedTags)

// IsSupported reports whether tag is exactly one of Supported.
func IsSupported(tag string) bool {
	for _, s := range Supported {
		if s == tag {
			return true
		}
	}
	return false
}

// Normalize maps a user-provided tag ("zh", "zh_CN.UTF-8", "en-US") onto a
// supported tag. The second result is false when no reasonable match exists.
func Normalize(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if i := strings.IndexAny(tag, ".@"); i >= 0 { // strip POSIX ".UTF-8" / "@euro"
		tag = tag[:i]
	}
	tag = strings.ReplaceAll(tag, "_", "-")
	if tag == "" || tag == "C" || tag == "POSIX" {
		return "", false
	}
	t, err := language.Parse(tag)
	if err != nil {
		return "", false
	}
	_, idx, conf := matcher.Match(t)
	if conf == language.No {
		return "", false
	}
	return Supported[idx], true
}

// Negotiate picks the best supported language from an Accept-Language header,
// falling back to fallback (and then to Default) when nothing matches.
func Negotiate(acceptLanguage, fallback string) string {
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err == nil && len(tags) > 0 {
		_, idx, conf := matcher.Match(tags...)
		if conf != language.No {
			return Supported[idx]
		}
	}
	if IsSupported(fallback) {
		return fallback
	}
	return Default
}

//go:embed locales/*.toml
var localeFS embed.FS

// Catalog renders backend messages.
type Catalog struct {
	bundle *goi18n.Bundle
}

// NewCatalog loads the embedded message files of every supported language.
func NewCatalog() (*Catalog, error) {
	b := goi18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	for _, tag := range Supported {
		if _, err := b.LoadMessageFileFS(localeFS, "locales/"+tag+".toml"); err != nil {
			return nil, fmt.Errorf("i18n: load %s: %w", tag, err)
		}
	}
	return &Catalog{bundle: b}, nil
}

// T renders message id in locale with the given template data. Missing
// translations fall back to English; an unknown id is returned unchanged so
// the problem is visible instead of silently empty.
func (c *Catalog) T(locale, id string, data map[string]any) string {
	loc := goi18n.NewLocalizer(c.bundle, locale, Default)
	msg, err := loc.Localize(&goi18n.LocalizeConfig{MessageID: id, TemplateData: data})
	if err != nil && msg == "" {
		return id
	}
	return msg
}
