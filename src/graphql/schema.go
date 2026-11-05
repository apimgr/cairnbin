// Package graphql provides a read-only GraphQL endpoint for the cairnbin API.
// Supported queries: paste(id: ID!) and pastes(page: Int, limit: Int).
// Mutations are intentionally absent; use the REST API for create and delete.
package graphql

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

// Field returns the fields present on the Paste type — used by introspection.
func pasteFields() []map[string]interface{} {
	return []map[string]interface{}{
		{"name": "id", "type": map[string]interface{}{"name": "ID", "kind": "SCALAR"}},
		{"name": "title", "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "content", "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "language", "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "is_link", "type": map[string]interface{}{"name": "Boolean", "kind": "SCALAR"}},
		{"name": "is_public", "type": map[string]interface{}{"name": "Boolean", "kind": "SCALAR"}},
		{"name": "created_at", "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "expires_at", "type": map[string]interface{}{"name": "String", "kind": "SCALAR"}},
		{"name": "burn_after_read", "type": map[string]interface{}{"name": "Int", "kind": "SCALAR"}},
		{"name": "view_count", "type": map[string]interface{}{"name": "Int", "kind": "SCALAR"}},
	}
}

// introspectionTypes returns the minimal __schema.types list for clients that
// call __schema { types { name } } introspection queries.
func introspectionTypes() []map[string]interface{} {
	return []map[string]interface{}{
		{"name": "Paste", "kind": "OBJECT", "fields": pasteFields()},
		{"name": "PasteList", "kind": "OBJECT"},
		{"name": "Query", "kind": "OBJECT"},
		{"name": "String", "kind": "SCALAR"},
		{"name": "ID", "kind": "SCALAR"},
		{"name": "Int", "kind": "SCALAR"},
		{"name": "Boolean", "kind": "SCALAR"},
	}
}
