// Package graphql provides a read-only GraphQL endpoint for the cairnbin API.
// Supported queries: paste(id: ID!) and pastes(page: Int, limit: Int).
// Mutations are intentionally absent; use the REST API for create and delete.
package graphql

import (
	"strings"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

// SchemaSDL is the GraphQL Schema Definition Language string for the API.
// It is generated from the resolver types defined in this package.
const SchemaSDL = `
"""A single paste entry."""
type Paste {
  """Unique paste identifier."""
  id: ID!
  """Optional human-readable title."""
  title: String!
  """Paste body text."""
  content: String!
  """Chroma syntax-highlighting language; meaningless when is_link is true."""
  language: String!
  """If true, content is a redirect target URL rather than paste text; fetching this paste's view route issues a 302 redirect."""
  is_link: Boolean!
  """Whether the paste is visible in public listings."""
  is_public: Boolean!
  """ISO-8601 creation timestamp."""
  created_at: String!
  """ISO-8601 expiry timestamp; null if the paste never expires."""
  expires_at: String
  """Delete after N views (0 = disabled)."""
  burn_after_read: Int!
  """Number of times the paste has been viewed."""
  view_count: Int!
}

"""Paginated list of pastes."""
type PasteList {
  """Current page contents."""
  pastes: [Paste!]!
  """Total number of public pastes across all pages."""
  total: Int!
  """The page number that was returned."""
  page: Int!
  """The page size that was used."""
  limit: Int!
}

type Query {
  """Retrieve a single paste by its ID."""
  paste(id: ID!): Paste
  """List recent public pastes, newest first."""
  pastes(page: Int, limit: Int): PasteList!
}
`

func localizedSchemaSDL(lang string) string {
	keys := []string{
		"schema_paste_type", "schema_paste_id", "schema_paste_title", "schema_paste_content",
		"schema_paste_language", "schema_paste_is_link", "schema_paste_is_public",
		"schema_paste_created_at", "schema_paste_expires_at", "schema_paste_burn_after_read",
		"schema_paste_view_count", "schema_paste_list_type", "schema_paste_list_pastes",
		"schema_paste_list_total", "schema_paste_list_page", "schema_paste_list_limit",
		"schema_query_paste", "schema_query_pastes",
	}
	originals := []string{
		"A single paste entry.", "Unique paste identifier.", "Optional human-readable title.",
		"Paste body text.", "Chroma syntax-highlighting language; meaningless when is_link is true.",
		"If true, content is a redirect target URL rather than paste text; fetching this paste view route issues a 302 redirect.",
		"Whether the paste is visible in public listings.", "ISO-8601 creation timestamp.",
		"ISO-8601 expiry timestamp; null if the paste never expires.", "Delete after N views (0 = disabled).",
		"Number of times the paste has been viewed.", "Paginated list of pastes.", "Current page contents.",
		"Total number of public pastes across all pages.", "The page number that was returned.",
		"The page size that was used.", "Retrieve a single paste by its ID.",
		"List recent public pastes, newest first.",
	}
	replacements := make([]string, 0, len(keys)*2)
	for i, key := range keys {
		replacements = append(replacements, originals[i], i18n.Translate(lang, "graphql."+key))
	}
	return strings.NewReplacer(replacements...).Replace(SchemaSDL)
}

// Field returns the fields present on the Paste type — used by introspection.
func pasteFields(lang string) []map[string]interface{} {
	d := func(key string) string { return i18n.Translate(lang, "graphql."+key) }
	return []map[string]interface{}{
		{"name": "id", "description": d("schema_paste_id"), "type": map[string]interface{}{"name": "ID", "kind": "SCALAR"}},
		{"name": "title", "description": d("schema_paste_title"), "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "content", "description": d("schema_paste_content"), "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "language", "description": d("schema_paste_language"), "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "is_link", "description": d("schema_paste_is_link"), "type": map[string]interface{}{"name": "Boolean", "kind": "SCALAR"}},
		{"name": "is_public", "description": d("schema_paste_is_public"), "type": map[string]interface{}{"name": "Boolean", "kind": "SCALAR"}},
		{"name": "created_at", "description": d("schema_paste_created_at"), "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "expires_at", "description": d("schema_paste_expires_at"), "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "burn_after_read", "description": d("schema_paste_burn_after_read"), "type": map[string]interface{}{"name": "Int", "kind": "SCALAR"}},
		{"name": "view_count", "description": d("schema_paste_view_count"), "type": map[string]interface{}{"name": "Int", "kind": "SCALAR"}},
	}
}

// introspectionTypes returns the minimal __schema.types list for clients that
// call __schema { types { name } } introspection queries.
func introspectionTypes(lang string) []map[string]interface{} {
	d := func(key string) string { return i18n.Translate(lang, "graphql."+key) }
	return []map[string]interface{}{
		{"name": "Paste", "description": d("schema_paste_type"), "kind": "OBJECT", "fields": pasteFields(lang)},
		{"name": "PasteList", "description": d("schema_paste_list_type"), "kind": "OBJECT"},
		{"name": "Query", "description": d("type_descriptions.query"), "kind": "OBJECT", "fields": []map[string]interface{}{
			{"name": "paste", "description": d("schema_query_paste"), "type": map[string]interface{}{"name": "Paste", "kind": "OBJECT"}},
			{"name": "pastes", "description": d("schema_query_pastes"), "type": map[string]interface{}{"name": "PasteList", "kind": "OBJECT"}},
		}},
		{"name": "String", "kind": "SCALAR"},
		{"name": "ID", "kind": "SCALAR"},
		{"name": "Int", "kind": "SCALAR"},
		{"name": "Boolean", "kind": "SCALAR"},
	}
}
