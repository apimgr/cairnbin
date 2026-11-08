package swagger

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

func TestLocalizedSpecUsesRequestLanguageAndKeepsOperationIDs(t *testing.T) {
	h := New("Cairnbin", "1.0.0", "https://example.test", "v1")
	routes := Routes("v1")
	translated := localizeRoutes(routes, "es")
	if translated[0].Summary != i18n.Translate("es", annotationKey(routes[0].Summary)) {
		t.Fatalf("localized summary = %q", translated[0].Summary)
	}
	if translated[0].Summary == routes[0].Summary {
		t.Fatal("Spanish OpenAPI summary remained in English")
	}
	if operationID(translated[0].Method, translated[0].Path) != operationID(routes[0].Method, routes[0].Path) {
		t.Fatal("localized annotations changed the stable operation ID")
	}

	spec := h.buildSpec("https://example.test", "es")
	paths := spec["paths"].(map[string]interface{})
	get := paths["/api/v1/pastes"].(map[string]interface{})["get"].(map[string]interface{})
	if get["summary"] != translated[0].Summary {
		t.Errorf("spec summary = %v, want %q", get["summary"], translated[0].Summary)
	}
}

func TestSwaggerUIUsesRequestedLocaleAndDirection(t *testing.T) {
	h := New("Cairnbin", "1.0.0", "", "v1")
	r := httptest.NewRequest("GET", "/server/docs/swagger?lang=ar", nil)
	body := h.renderUI(r, "/api/swagger")
	if !strings.Contains(body, `lang="ar" dir="rtl"`) {
		t.Error("Arabic Swagger UI did not render the RTL language direction")
	}
	if !strings.Contains(body, i18n.Translate("ar", "swagger.loading")) {
		t.Error("Swagger UI loading text was not translated")
	}
}

func TestServeSpecResolvesRequestLanguage(t *testing.T) {
	h := New("Cairnbin", "1.0.0", "https://example.test", "v1")
	h.SetLanguageResolver(i18n.LangFromRequest)
	r := httptest.NewRequest(http.MethodGet, "/api/swagger?lang=es", nil)
	w := httptest.NewRecorder()
	h.ServeSpec(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("spec status = %d", w.Code)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	paths := spec["paths"].(map[string]interface{})
	operation := paths["/api/v1/pastes"].(map[string]interface{})["get"].(map[string]interface{})
	if operation["summary"] != i18n.Translate("es", annotationKey("List public pastes")) {
		t.Errorf("summary = %v, want Spanish translation", operation["summary"])
	}
}
