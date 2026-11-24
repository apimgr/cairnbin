package server

import (
	"net/http"
	"strings"
)

func (s *Server) handleHelpMCP(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	apiPath := strings.HasPrefix(r.URL.Path, "/api/")
	base := strings.TrimRight(s.baseURL(r), "/")
	version := s.apiVersion()
	url := base + "/api/" + version + "/server/mcp"
	project := cfg.Server.Pages.Help.MCP
	if apiPath {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true,
			"data": map[string]interface{}{
				"enabled": true,
				"url":     url,
				"aliases": []string{base + "/api/mcp", base + "/mcp"},
				"auth":    map[string]string{"scheme": "Bearer"},
				"tools":   s.publicMCPTools(),
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
		"MCPToolsView": s.mcpToolsView(),
		"MCPProject":   pageContentHTML(project),
		"ProjectName":  "cairnbin",
	}
	s.renderTemplate(w, r, "help_mcp.html", data)
}

func (s *Server) mcpToolsView() []map[string]interface{} {
	tools := s.publicMCPTools()
	view := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		view = append(view, map[string]interface{}{
			"name":        tool["name"],
			"description": renderMarkdown(tool["description"]),
		})
	}
	return view
}

func (s *Server) publicMCPTools() []map[string]string {
	if s.mcpHandler == nil {
		return []map[string]string{}
	}
	return s.mcpHandler.PublicTools()
}
