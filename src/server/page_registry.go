package server

import (
	"sort"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

type hubPageDefinition struct {
	Path           string
	TitleKey       string
	DescriptionKey string
	Order          int
}

type hubPageView struct {
	Path        string
	Title       string
	Description string
}

func (s *Server) preferencePageRegistry() []hubPageDefinition {
	pages := []hubPageDefinition{{
		Path: "/server/preferences/sync", TitleKey: "preferences.sync_title",
		DescriptionKey: "preferences.sync_desc", Order: 10,
	}}
	if s.liveCfg().Server.Tokens.MaxLinked > 0 {
		pages = append(pages, hubPageDefinition{
			Path: "/server/preferences/tokens", TitleKey: "preferences.owner_tokens",
			DescriptionKey: "preferences.tokens_desc", Order: 20,
		})
	}
	return pages
}

func (s *Server) helpPageRegistry() []hubPageDefinition {
	return []hubPageDefinition{{
		Path: "/server/help/mcp", TitleKey: "help.mcp_page_title",
		DescriptionKey: "help.mcp_meta_description", Order: 10,
	}}
}

func (s *Server) hubPageData(pages []hubPageDefinition, lang string) []hubPageView {
	ordered := append([]hubPageDefinition(nil), pages...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	out := make([]hubPageView, 0, len(ordered))
	for _, page := range ordered {
		title := i18n.Translate(lang, page.TitleKey)
		description := i18n.Translate(lang, page.DescriptionKey)
		if page.DescriptionKey == "help.mcp_meta_description" {
			description = i18n.TranslateFormat(lang, page.DescriptionKey, map[string]string{"site": s.liveCfg().Web.SiteTitle})
		}
		out = append(out, hubPageView{Path: page.Path, Title: title, Description: description})
	}
	return out
}

func nativeLanguageName(code string) string {
	for _, language := range i18n.Languages() {
		if language.Code == code {
			return language.NativeName
		}
	}
	return code
}
