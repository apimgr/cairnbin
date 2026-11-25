package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/graphql"
	"github.com/apimgr/cairnbin/src/handler"
	"github.com/apimgr/cairnbin/src/handler/compat"
	"github.com/apimgr/cairnbin/src/mcp"
	"github.com/apimgr/cairnbin/src/metric"
	"github.com/apimgr/cairnbin/src/swagger"
	"github.com/go-chi/chi/v5"
)

func TestRegisteredPagesConform(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Pages.Contact.Enabled = true
	cfg.Server.Tokens.MaxLinked = 1
	s := newMinimalServer(cfg)
	statusTokenHash := sha256.Sum256([]byte("conformance-token"))
	s.db = &pageConformanceDB{
		stubDB: &stubDB{},
		report: &database.SecurityReport{
			TrackingID: "sec_conformance", Status: database.SecStatusReceived,
			Severity: "Low", Component: "web", TokenHash: hex.EncodeToString(statusTokenHash[:]),
			DisclosureDays: 90, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		},
	}
	s.csrfSecret = bytes.Repeat([]byte{0x5a}, 32)
	templates, err := s.buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	s.templates = templates
	s.swaggerHandler = swagger.New(cfg.Web.SiteTitle+" API", "test", "", "v1")
	s.graphqlHandler = graphql.New(nil, cfg.Web.SiteTitle)
	pageRenderer := func(w http.ResponseWriter, r *http.Request, title, lead, section, content string) {
		data := s.pageData()
		data["ViewerTitle"], data["ViewerLead"], data["ViewerSection"], data["ViewerContent"] = title, lead, section, template.HTML(content)
		s.renderTemplate(w, r, "docs_viewer.html", data)
	}
	s.swaggerHandler.SetPageRenderer(pageRenderer)
	s.graphqlHandler.SetPageRenderer(pageRenderer)

	pages := map[string]http.HandlerFunc{
		"/server/about":           s.handleAbout,
		"/server/dpo":             s.handleDPO,
		"/server/contact":         s.handleContact,
		"/server/privacy":         s.handlePrivacy,
		"/server/terms":           s.handleTerms,
		"/server/security":        s.handleSecurityOverview,
		"/server/security/policy": s.handleSecurityPolicy,
		"/server/security/thanks": s.handleSecurityThanks,
		"/server/security/report/sec_conformance": func(w http.ResponseWriter, r *http.Request) {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("tracking_id", "sec_conformance")
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
			r.URL.RawQuery = "token=conformance-token"
			s.handleSecurityReportStatus(w, r)
		},
		"/server/healthz":            s.handleHealthz,
		"/server/preferences":        s.handlePreferences,
		"/server/preferences/sync":   s.handlePreferencesExport,
		"/server/preferences/tokens": s.handleOwnerTokensPage,
		"/server/help":               s.handleHelp,
		"/server/help/mcp":           s.handleHelpMCP,
		"/server/docs/swagger":       s.swaggerHandler.ServeUI,
		"/server/docs/graphql":       s.graphqlHandler.ServeHTTP,
	}
	pageHTML := make(map[string]string, len(pages)+1)
	for path, handler := range pages {
		if strings.HasSuffix(path, "/tokens") && cfg.Server.Tokens.MaxLinked == 0 {
			continue
		}
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
		request.Header.Set("Accept", "text/html")
		request.Header.Set("User-Agent", "Mozilla/5.0")
		s.csrfMiddleware(handler).ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", path, response.Code, response.Body.String())
		}
		pageHTML[path] = response.Body.String()
		assertPageConformance(t, path, response.Body.String())
	}
	configurePageConformanceRouter(s, cfg)
	for path, body := range pageHTML {
		assertInternalPageLinksResolve(t, path, body, s.router)
	}

	if cfg.Server.Branding.Content == nil {
		cfg.Server.Branding.Content = make(map[string]config.BrandingContent)
	}
	cfg.Server.Branding.Content["header"] = config.BrandingContent{
		Format: "html", Body: `<span class="operator-header-content">Operator header content</span>`,
	}
	customHeaderResponse := httptest.NewRecorder()
	customHeaderRequest := httptest.NewRequest(http.MethodGet, "http://example.test/server/about", nil)
	customHeaderRequest.Header.Set("Accept", "text/html")
	customHeaderRequest.Header.Set("User-Agent", "Mozilla/5.0")
	s.handleAbout(customHeaderResponse, customHeaderRequest)
	assertPageConformance(t, "/server/about with operator header content", customHeaderResponse.Body.String())

	for _, hub := range []struct {
		path  string
		pages []hubPageDefinition
	}{{"/server/preferences", s.preferencePageRegistry()}, {"/server/help", s.helpPageRegistry()}} {
		body := pageHTML[hub.path]
		for _, entry := range hub.pages {
			if !strings.Contains(body, `href="`+entry.Path+`"`) {
				t.Errorf("hub %s omits registered page %s", hub.path, entry.Path)
			}
			if strings.Count(body, `href="`+entry.Path+`"`) != 1 {
				t.Errorf("hub %s must link %s exactly once", hub.path, entry.Path)
			}
			if _, ok := pageHTML[entry.Path]; !ok {
				t.Errorf("registered page %s was not rendered", entry.Path)
			}
		}
	}

	{
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/server/help", nil)
		s.handleHelpAPI(response, request)
		var payload struct {
			Data struct {
				Pages []struct {
					Path string `json:"path"`
				} `json:"pages"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("parse help API: %v", err)
		}
		found := false
		for _, page := range payload.Data.Pages {
			if page.Path == "/server/help/mcp" {
				found = true
			}
		}
		if !found {
			t.Error("help API page registry omits /server/help/mcp")
		}
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/not-found", nil)
	request.Header.Set("Accept", "text/html")
	request.Header.Set("User-Agent", "Mozilla/5.0")
	s.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.renderErrorPage(w, r, http.StatusNotFound, "Not found")
	})).ServeHTTP(response, request)
	assertPageConformance(t, "error", response.Body.String())
}

type pageConformanceDB struct {
	*stubDB
	report *database.SecurityReport
}

func (d *pageConformanceDB) GetSecurityReport(trackingID string) (*database.SecurityReport, error) {
	if trackingID == d.report.TrackingID {
		return d.report, nil
	}
	return nil, nil
}

func configurePageConformanceRouter(s *Server, cfg *config.Config) {
	s.router = chi.NewRouter()
	s.metricsCollector = metric.NewWithOptions(metric.Options{})
	s.pasteHandler = handler.NewPasteHandler(s.db, cfg.Server.BaseURL, [32]byte{})
	s.compatHandler = compat.New(s.pasteHandler, s.db, "test")
	s.mcpHandler = mcp.New(mcp.Config{APIVersion: "v1", Dispatch: s.router})
	s.setupRoutes()
}

func assertInternalPageLinksResolve(t *testing.T, page, source string, router chi.Router) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatalf("parse %s HTML links: %v", page, err)
	}
	check := func(rawURL, method, kind string) {
		if rawURL == "" {
			return
		}
		reference, err := url.Parse(rawURL)
		if err != nil {
			t.Errorf("%s has invalid %s URL %q: %v", page, kind, rawURL, err)
			return
		}
		if reference.IsAbs() || reference.Host != "" {
			return
		}
		base, _ := url.Parse("https://example.test" + page)
		resolved := base.ResolveReference(reference)
		routeContext := chi.NewRouteContext()
		if !router.Match(routeContext, method, resolved.Path) {
			t.Errorf("%s %s %q does not resolve to an application route", page, kind, rawURL)
		}
	}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "a":
				check(attr(node, "href"), http.MethodGet, "link")
			case "form":
				method := strings.ToUpper(attr(node, "method"))
				if method == "" {
					method = http.MethodGet
				}
				check(attr(node, "action"), method, "form action")
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
}

func assertPageConformance(t *testing.T, name, source string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatalf("parse %s HTML: %v", name, err)
	}
	count := func(tag string) int {
		n := 0
		var walk func(*html.Node)
		walk = func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == tag {
				n++
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
		}
		walk(doc)
		return n
	}
	if count("main") != 1 || count("h1") != 1 {
		t.Errorf("%s must have exactly one main and one h1; got %d main, %d h1", name, count("main"), count("h1"))
	}
	if strings.Contains(source, "{app_name}") {
		t.Errorf("%s exposes an untranslated template placeholder", name)
	}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if node.Data == "style" {
				t.Errorf("%s contains a style element", name)
			}
			if node.Data == "script" && attr(node, "src") == "" && attr(node, "type") != "application/ld+json" && strings.TrimSpace(nodeText(node)) != "" {
				t.Errorf("%s contains an inline script body", name)
			}
			if node.Data == "img" && attr(node, "alt") == "" {
				t.Errorf("%s contains image %q without alt text", name, attr(node, "src"))
			}
			if node.Data == "form" && strings.EqualFold(attr(node, "method"), "post") && !hasDescendantInput(node, "csrf_token") {
				t.Errorf("%s contains a POST form without csrf_token", name)
			}
			if node.Data == "table" {
				if firstChildByTag(node, "thead") == nil || !hasAncestorClass(node, "table-wrapper") {
					t.Errorf("%s contains a table without thead inside .table-wrapper", name)
				}
			}
			for _, attribute := range node.Attr {
				if attribute.Key == "style" || strings.HasPrefix(strings.ToLower(attribute.Key), "on") {
					t.Errorf("%s contains forbidden %s attribute", name, attribute.Key)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if !strings.Contains(source, `class="page-title"`) {
		t.Errorf("%s does not use .page-title", name)
	}
	assertSkipLinksFirst(t, name, doc)
	assertFormControlLabels(t, name, doc)
	assertHeaderConformance(t, name, doc)
	assertPageShell(t, name, doc)
}

func assertPageShell(t *testing.T, name string, root *html.Node) {
	t.Helper()
	containers := nodesByClass(root, "container")
	if len(containers) != 1 {
		t.Errorf("%s must use exactly one shared .container, got %d", name, len(containers))
	} else {
		widths := 0
		for _, width := range []string{"container-sm", "container-md", "container-lg"} {
			if hasClass(containers[0], width) {
				widths++
			}
		}
		if widths != 1 {
			t.Errorf("%s container must have exactly one width modifier", name)
		}
	}
	for _, card := range nodesByClass(root, "section-card") {
		label := attr(card, "aria-labelledby")
		if label == "" || !hasDescendantID(card, label) || countDescendantTag(card, "h2") != 1 {
			t.Errorf("%s has a section card without exactly one labelled h2 (label=%q ids=%v h2=%d)", name, label, nodeIDs(card), countDescendantTag(card, "h2"))
		}
	}
	wantHub := ""
	switch name {
	case "/server/preferences/sync", "/server/preferences/tokens":
		wantHub = "/server/preferences"
	case "/server/help/mcp":
		wantHub = "/server/help"
	case "/server/security/policy", "/server/security/thanks":
		wantHub = "/server/security"
	}
	if strings.HasPrefix(name, "/server/security/report/") {
		wantHub = "/server/security"
	}
	crumbs := nodesByClass(root, "breadcrumb")
	if wantHub == "" {
		if len(crumbs) != 0 {
			t.Errorf("%s must not have a breadcrumb", name)
		}
		return
	}
	if len(crumbs) != 1 {
		t.Errorf("%s must have exactly one breadcrumb", name)
		return
	}
	links := nodesByTag(crumbs[0], "a")
	if len(links) != 1 || attr(links[0], "href") != wantHub {
		t.Errorf("%s breadcrumb must link once to %s", name, wantHub)
	}
}

func nodeIDs(root *html.Node) []string {
	var ids []string
	for _, node := range nodesByTag(root, "h2") {
		ids = append(ids, attr(node, "id"))
	}
	return ids
}

func nodesByClass(root *html.Node, class string) []*html.Node {
	var nodes []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && hasClass(node, class) {
			nodes = append(nodes, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return nodes
}

func nodesByTag(root *html.Node, tag string) []*html.Node {
	var nodes []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == tag {
			nodes = append(nodes, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return nodes
}

func hasDescendantID(root *html.Node, id string) bool {
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && attr(child, "id") == id {
			return true
		}
		if hasDescendantID(child, id) {
			return true
		}
	}
	return false
}

func countDescendantTag(root *html.Node, tag string) int {
	count := 0
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == tag {
			count++
		}
		count += countDescendantTag(child, tag)
	}
	return count
}

func attr(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

func nodeText(node *html.Node) string {
	var value strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			value.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return value.String()
}

func hasDescendantInput(node *html.Node, name string) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "input" && attr(child, "name") == name {
			return true
		}
		if hasDescendantInput(child, name) {
			return true
		}
	}
	return false
}

func firstChildByTag(node *html.Node, tag string) *html.Node {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == tag {
			return child
		}
	}
	return nil
}

func hasAncestorClass(node *html.Node, class string) bool {
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if parent.Type != html.ElementNode {
			continue
		}
		for _, value := range strings.Fields(attr(parent, "class")) {
			if value == class {
				return true
			}
		}
	}
	return false
}

func assertSkipLinksFirst(t *testing.T, name string, root *html.Node) {
	t.Helper()
	var focusable []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			focusableElement := false
			switch node.Data {
			case "a":
				focusableElement = attr(node, "href") != ""
			case "button", "select", "textarea", "summary":
				focusableElement = true
			case "input":
				focusableElement = attr(node, "type") != "hidden"
			}
			if tabindex := attr(node, "tabindex"); tabindex != "" && tabindex != "-1" {
				focusableElement = true
			}
			if focusableElement {
				focusable = append(focusable, node)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if len(focusable) < 2 || attr(focusable[0], "class") != "skip-link" || attr(focusable[0], "href") != "#main-content" || attr(focusable[1], "class") != "skip-link" || attr(focusable[1], "href") != "#navigation" {
		t.Errorf("%s must begin with skip-to-content and skip-to-navigation as its first focusable controls", name)
	}
}

func assertFormControlLabels(t *testing.T, name string, root *html.Node) {
	t.Helper()
	ids := make(map[string]bool)
	labelledIDs := make(map[string]bool)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if id := attr(node, "id"); id != "" {
				ids[id] = true
			}
			if node.Data == "label" {
				if target := attr(node, "for"); target != "" {
					labelledIDs[target] = true
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == html.ElementNode && (child.Data == "input" || child.Data == "select" || child.Data == "textarea") {
						labelledIDs[attr(child, "id")] = true
					}
				}
			}
			if node.Data == "input" && attr(node, "type") != "hidden" {
				if attr(node, "aria-label") == "" && attr(node, "aria-labelledby") == "" && !labelledIDs[attr(node, "id")] {
					if !hasLabelAncestor(node) {
						t.Errorf("%s contains an input without a label", name)
					}
				}
			}
			if node.Data == "select" || node.Data == "textarea" {
				if attr(node, "aria-label") == "" && attr(node, "aria-labelledby") == "" && !labelledIDs[attr(node, "id")] && !hasLabelAncestor(node) {
					t.Errorf("%s contains a %s without a label", name, node.Data)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	for id := range labelledIDs {
		if id != "" && !ids[id] {
			t.Errorf("%s has a label for missing control %q", name, id)
		}
	}
}

func hasLabelAncestor(node *html.Node) bool {
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if parent.Type == html.ElementNode && parent.Data == "label" {
			return true
		}
	}
	return false
}

func assertHeaderConformance(t *testing.T, name string, root *html.Node) {
	t.Helper()
	var header *html.Node
	var find func(*html.Node)
	find = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "header" && hasClass(node, "header") {
			header = node
			return
		}
		for child := node.FirstChild; child != nil && header == nil; child = child.NextSibling {
			find(child)
		}
	}
	find(root)
	if header == nil {
		t.Errorf("%s has no shared header", name)
		return
	}
	checkboxes := nodesByClass(root, "nav-checkbox")
	if len(checkboxes) != 1 {
		t.Errorf("%s must have exactly one shared navigation checkbox, got %d", name, len(checkboxes))
	} else if checkboxes[0].Parent != header.Parent || nextElementSibling(checkboxes[0]) != header {
		t.Errorf("%s navigation checkbox must be a body sibling immediately before the header", name)
	}
	var children []*html.Node
	for child := header.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			children = append(children, child)
		}
	}
	if len(children) < 4 || children[0].Data != "a" || !hasClass(children[0], "site-brand") || children[1].Data != "nav" || !hasClass(children[1], "nav-links") || children[2].Data != "label" || !hasClass(children[2], "nav-toggle") || children[3].Data != "div" || !hasClass(children[3], "header-actions") {
		t.Errorf("%s header does not follow the shared brand, navigation, menu, actions order", name)
		return
	}
	if len(children) == 4 && strings.Contains(name, "operator header content") {
		t.Errorf("%s omitted the configured operator header fragment", name)
	}
	if len(children) > 4 && (len(children) != 5 || !hasClass(children[len(children)-1], "operator-header-content")) {
		t.Errorf("%s has unexpected header content after the shared header actions", name)
	}
}

func nextElementSibling(node *html.Node) *html.Node {
	for sibling := node.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		if sibling.Type == html.ElementNode {
			return sibling
		}
	}
	return nil
}

func hasClass(node *html.Node, class string) bool {
	for _, value := range strings.Fields(attr(node, "class")) {
		if value == class {
			return true
		}
	}
	return false
}
