package graphql

import (
	"strings"

	"github.com/apimgr/cairnbin/src/model"
)

// DB is the minimal database interface required by the resolvers.
// It matches the subset of database.DB that the GraphQL layer needs.
type DB interface {
	GetPasteByID(id string) (*model.Paste, error)
	GetPublicPastes(page, limit int) ([]model.PasteListItem, int, error)
}

// Resolver executes GraphQL queries against the database.
type Resolver struct {
	db DB
}

// NewResolver creates a Resolver backed by the given database.
func NewResolver(db DB) *Resolver {
	return &Resolver{db: db}
}

// Resolve dispatches a parsed query to the appropriate resolver method.
// Returns data and a (possibly empty) slice of error objects.
func (res *Resolver) Resolve(query string, vars map[string]interface{}) (interface{}, []map[string]interface{}) {
	q := strings.TrimSpace(query)

	// __schema introspection
	if strings.Contains(q, "__schema") || strings.Contains(q, "__type") {
		return map[string]interface{}{
			"__schema": map[string]interface{}{
				"types":            introspectionTypes(),
				"queryType":        map[string]interface{}{"name": "Query"},
				"mutationType":     nil,
				"subscriptionType": nil,
			},
		}, nil
	}

	// paste(id: "…") { … }
	if strings.Contains(q, "paste(") {
		id, _ := vars["id"].(string)
		if id == "" {
			id = extractInlineArg(q, "id")
		}
		if id == "" {
			return nil, errList("id is required for paste query")
		}
		return res.resolvePaste(id)
	}

	// pastes(page: N, limit: N) { … }
	page := intVar(vars, "page", 1)
	limit := intVar(vars, "limit", 20)
	return res.resolvePastes(page, limit)
}

// resolvePaste returns a single paste by ID.
func (res *Resolver) resolvePaste(id string) (interface{}, []map[string]interface{}) {
	paste, err := res.db.GetPasteByID(id)
	if err != nil || paste == nil {
		return nil, errList("paste not found")
	}
	// Return only the safe subset — DeleteTokenHash is tagged `json:"-"` so
	// it is already excluded from JSON encoding, but clear it defensively.
	paste.DeleteTokenHash = ""
	return map[string]interface{}{"paste": paste}, nil
}

// resolvePastes returns a paginated list of public pastes.
func (res *Resolver) resolvePastes(page, limit int) (interface{}, []map[string]interface{}) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if page < 1 {
		page = 1
	}
	pastes, total, err := res.db.GetPublicPastes(page, limit)
	if err != nil {
		return nil, errList("database error")
	}
	// PasteListItem has no sensitive fields — safe to return directly.
	return map[string]interface{}{
		"pastes": map[string]interface{}{
			"pastes": pastes,
			"total":  total,
			"page":   page,
			"limit":  limit,
		},
	}, nil
}

// extractInlineArg extracts the value of a named argument from an inline query
// string like `paste(id: "abc")`.
func extractInlineArg(q, name string) string {
	needle := name + ":"
	i := strings.Index(q, needle)
	if i == -1 {
		return ""
	}
	rest := strings.TrimSpace(q[i+len(needle):])
	if len(rest) == 0 {
		return ""
	}
	if rest[0] == '"' || rest[0] == '\'' {
		rest = rest[1:]
		if j := strings.IndexAny(rest, `"'`); j != -1 {
			return rest[:j]
		}
	}
	// Bare value (e.g. integer).
	if j := strings.IndexAny(rest, " \t\n\r,)"); j != -1 {
		return rest[:j]
	}
	return rest
}

// intVar extracts an integer variable from the vars map with a fallback default.
func intVar(vars map[string]interface{}, key string, def int) int {
	if v, ok := vars[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

// errList wraps a message in the GraphQL errors array format.
func errList(msg string) []map[string]interface{} {
	return []map[string]interface{}{{"message": msg}}
}
