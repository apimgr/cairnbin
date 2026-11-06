package server

import (
	"net/http"
	"strings"
)

func (s *Server) handleHelpMCP(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	if !cfg.Server.MCP.Enabled {
		http.NotFound(w, r)
		return
	}
	base := strings.TrimRight(s.baseURL(r), "/")
	version := s.apiVersion()
	url := base + "/api/" + version + "/server/mcp"
	project := cfg.Server.Pages.Help.MCP
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true,
			"data": map[string]interface{}{
				"enabled": true,
				"url":     url,
				"aliases": []string{base + "/api/mcp", base + "/mcp"},
				"auth":    map[string]string{"scheme": "Bearer"},
				"tools":   s.mcpHandler.PublicTools(),
				"project": project,
			},
		})
		return
	}
	data := map[string]interface{}{
		"SiteTitle":    cfg.Web.SiteTitle,
		"Theme":        cfg.Web.Theme,
		"BaseURL":      base,
		"MCPURL":       url,
		"MCPAliasAPI":  base + "/api/mcp",
		"MCPAliasRoot": base + "/mcp",
		"MCPTools":     s.mcpHandler.PublicTools(),
		"MCPProject":   pageContentHTML(project),
		"ProjectName":  "cairnbin",
	}
	s.renderTemplate(w, r, "help_mcp.html", data)
}
