package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/mcp"
)

func TestHandleHelpMCPDisabledReturnsNotFound(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.MCP.Enabled = false
	s := newMinimalServer(cfg)
	for _, path := range []string{"/server/help/mcp", "/api/v1/server/help/mcp"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.handleHelpMCP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, w.Code)
		}
	}
}

func TestHandleHelpMCPJSON(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.MCP.Enabled = true
	cfg.Server.Pages.Help.MCP = "## Project instructions"
	s := newMinimalServer(cfg)
	s.mcpHandler = mcp.New(mcp.Config{APIVersion: "v1"})
	r := httptest.NewRequest(http.MethodGet, "http://example.com/api/v1/server/help/mcp", nil)
	w := httptest.NewRecorder()
	s.handleHelpMCP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Enabled bool                `json:"enabled"`
			URL     string              `json:"url"`
			Aliases []string            `json:"aliases"`
			Auth    map[string]string   `json:"auth"`
			Tools   []map[string]string `json:"tools"`
			Project string              `json:"project"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || !response.Data.Enabled || response.Data.URL != "http://example.com/api/v1/server/mcp" {
		t.Fatalf("unexpected MCP help response: %+v", response)
	}
	if len(response.Data.Aliases) != 2 || response.Data.Aliases[0] != "http://example.com/api/mcp" || response.Data.Auth["scheme"] != "Bearer" {
		t.Errorf("unexpected aliases/auth: %+v %+v", response.Data.Aliases, response.Data.Auth)
	}
	if len(response.Data.Tools) == 0 || response.Data.Project != "## Project instructions" {
		t.Errorf("tools/project missing: %+v", response.Data)
	}
	tools := make(map[string]bool, len(response.Data.Tools))
	for _, tool := range response.Data.Tools {
		tools[tool["name"]] = true
	}
	for _, name := range []string{
		"getApiV1Pastes", "postApiV1Pastes", "getApiV1PastesId", "getApiV1PastesIdRaw",
		"deleteApiV1PastesId", "getApiV1ServerHealthz", "getApiV1ServerVersion",
	} {
		if !tools[name] {
			t.Errorf("live tool catalogue missing %q", name)
		}
	}
}

func TestHandleHelpMCPHTMLSectionsAndHelpLink(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.MCP.Enabled = true
	cfg.Server.Pages.Help.MCP = "## Project instructions"
	s := newMinimalServer(cfg)
	s.mcpHandler = mcp.New(mcp.Config{APIVersion: "v1"})
	s.templates, _ = s.buildTemplates()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/server/help/mcp", nil)
	w := httptest.NewRecorder()
	s.handleHelpMCP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("MCP page status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	last := -1
	for _, id := range []string{"overview", "endpoint", "authentication", "connect", "tools", "limits", "troubleshooting", "project"} {
		pos := strings.Index(body, `id="`+id+`"`)
		if pos <= last {
			t.Fatalf("section %q missing or out of order", id)
		}
		last = pos
	}
	if !strings.Contains(body, "Authorization: Bearer") || !strings.Contains(body, "user-select-all") {
		t.Error("page omits the Bearer guidance or selectable endpoint")
	}

	data := map[string]interface{}{"SiteTitle": cfg.Web.SiteTitle, "Theme": cfg.Web.Theme, "BaseURL": "http://example.com", "MCPEnabled": true}
	linkPage, err := s.renderTemplateToString(r, "help.html", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(linkPage, `href="/server/help/mcp"`) {
		t.Error("/server/help does not link to enabled MCP help")
	}
	data["MCPEnabled"] = false
	linkPage, err = s.renderTemplateToString(r, "help.html", data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(linkPage, `href="/server/help/mcp"`) {
		t.Error("/server/help links to MCP help while MCP is disabled")
	}
}
