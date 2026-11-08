package graphql

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalizedSchemaSDLUsesRequestedLanguage(t *testing.T) {
	got := localizedSchemaSDL("es")
	if !strings.Contains(got, "Una entrada de texto.") || !strings.Contains(got, "Identificador único del texto.") {
		t.Fatalf("Spanish schema descriptions are missing: %q", got)
	}
	if strings.Contains(got, "A single paste entry.") {
		t.Fatalf("English schema description remained in Spanish schema: %q", got)
	}
}

func TestGraphQLUIUsesRequestedLanguage(t *testing.T) {
	h := New(nil, "Cairnbin")
	r := httptest.NewRequest("GET", "/server/docs/graphql?lang=es", nil)
	got := h.renderUI(r)
	for _, want := range []string{"Ejecutar", "Variables (JSON)", "Respuesta", "Una entrada de texto."} {
		if !strings.Contains(got, want) {
		t.Errorf("Spanish GraphQL UI is missing %q", want)
		}
	}
}

func TestGraphQLUIEscapesBrandAndSetsRTLDirection(t *testing.T) {
	h := New(nil, `<script>alert(1)</script>`)
	r := httptest.NewRequest("GET", "/server/docs/graphql?lang=ar", nil)
	got := h.renderUI(r)
	if !strings.Contains(got, `dir="rtl"`) || strings.Contains(got, `<script>alert(1)</script>`) {
		t.Fatalf("Arabic GraphQL UI direction/escaping incorrect: %q", got)
	}
}

func TestIntrospectionDescriptionsUseRequestedLanguage(t *testing.T) {
	data, errs := NewResolver(nil).ResolveLocalized("{ __schema { types { name description } } }", nil, "es")
	if len(errs) != 0 {
		t.Fatalf("introspection returned errors: %v", errs)
	}
	schema := data.(map[string]interface{})["__schema"].(map[string]interface{})
	types := schema["types"].([]map[string]interface{})
	for _, typ := range types {
		if typ["name"] == "Paste" && typ["description"] != "Una entrada de texto." {
			t.Fatalf("localized Paste description = %v", typ["description"])
		}
	}
}
