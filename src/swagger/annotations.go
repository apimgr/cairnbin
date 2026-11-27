package swagger

import "strings"

// Route describes a single API operation for the generated OpenAPI spec.
type Route struct {
	Method      string
	Path        string
	Tag         string
	Summary     string
	Description string
	Params      []Param
	Body        *BodySpec
	Responses   map[int]ResponseSpec
}

// Param describes an operation parameter (path, query, header).
type Param struct {
	Name string
	// "path", "query", "header"
	In          string
	Required    bool
	Description string
	Schema      map[string]interface{}
}

// BodySpec describes a request body.
type BodySpec struct {
	Required    bool
	Description string
	ContentType string
	Schema      map[string]interface{}
}

// ResponseSpec describes an HTTP response.
type ResponseSpec struct {
	Description string
	ContentType string
	Schema      map[string]interface{}
}

// Routes returns the canonical API route annotations for the cairnbin service.
// These are the authoritative source from which the OpenAPI JSON spec is generated.
// Compatibility routes are documented with the "compatibility" tag; they accept the
// same wire formats as their respective upstream services so existing scripts work
// without modification (AI.md PART 14).
func Routes(apiVersion string) []Route {
	apiBase := "/api/" + apiVersion

	pasteSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":              map[string]interface{}{"type": "string", "example": "AbCdEfGh"},
			"title":           map[string]interface{}{"type": "string", "example": "My Paste"},
			"content":         map[string]interface{}{"type": "string", "example": "hello world"},
			"language":        map[string]interface{}{"type": "string", "example": "go"},
			"is_link":         map[string]interface{}{"type": "boolean", "example": false, "description": "If true, content is a redirect target URL, not paste text"},
			"is_public":       map[string]interface{}{"type": "boolean", "example": true},
			"created_at":      map[string]interface{}{"type": "string", "format": "date-time"},
			"expires_at":      map[string]interface{}{"type": "string", "format": "date-time", "nullable": true},
			"burn_after_read": map[string]interface{}{"type": "integer", "example": 0},
			"view_count":      map[string]interface{}{"type": "integer", "example": 42},
		},
	}

	pasteListSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pastes": map[string]interface{}{
				"type":  "array",
				"items": pasteSchema,
			},
			"total": map[string]interface{}{"type": "integer", "example": 100},
			"page":  map[string]interface{}{"type": "integer", "example": 1},
			"limit": map[string]interface{}{"type": "integer", "example": 20},
		},
	}

	createSchema := map[string]interface{}{
		"type":     "object",
		"required": []string{"content"},
		"properties": map[string]interface{}{
			"content":    map[string]interface{}{"type": "string", "description": "Paste body text. If the entire trimmed content is exactly one http:// or https:// URL and nothing else, the paste is auto-detected as a link (is_link=true in the response) — there is no request field to set this explicitly"},
			"title":      map[string]interface{}{"type": "string", "description": "Optional title"},
			"language":   map[string]interface{}{"type": "string", "description": "Chroma language identifier; omit for auto-detect; ignored when content is auto-detected as a link"},
			"is_public":  map[string]interface{}{"type": "boolean", "default": true, "description": "false = unlisted"},
			"expires_in": map[string]interface{}{"type": "string", "example": "1d", "description": "Duration: 1h 1d 1w 1m 3m 6m 1y 18m 2y never"},
			"burn_after": map[string]interface{}{"type": "integer", "default": 0, "description": "Delete after N views (0 = disabled; max 9999)"},
		},
	}

	errSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"error": map[string]interface{}{"type": "string"},
		},
	}

	routes := []Route{
		{
			Method:      "GET",
			Path:        apiBase + "/pastes",
			Tag:         "pastes",
			Summary:     "List public pastes",
			Description: "Returns recent public pastes by default. Use scope=owned with a valid owner Bearer token to list all of that owner's non-expired pastes, including unlisted pastes.",
			Params: []Param{
				{Name: "page", In: "query", Required: false, Description: "Page number (default 1)", Schema: map[string]interface{}{"type": "integer", "default": 1}},
				{Name: "limit", In: "query", Required: false, Description: "Items per page (1–250, default 250)", Schema: map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 250, "default": 250}},
				{Name: "scope", In: "query", Required: false, Description: "public (default) or owned; owned requires an owner Bearer token", Schema: map[string]interface{}{"type": "string", "enum": []string{"public", "owned"}, "default": "public"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "OK", ContentType: "application/json", Schema: pasteListSchema},
				400: {Description: "Invalid scope", ContentType: "application/json", Schema: errSchema},
				401: {Description: "Owner token required for scope=owned", ContentType: "application/json", Schema: errSchema},
				500: {Description: "Internal server error", ContentType: "application/json", Schema: errSchema},
			},
		},
		{
			Method:      "POST",
			Path:        apiBase + "/pastes",
			Tag:         "pastes",
			Summary:     "Create paste",
			Description: "Creates a new paste. Accepts JSON or multipart/form-data.",
			Body: &BodySpec{
				Required:    true,
				Description: "Paste creation request",
				ContentType: "application/json",
				Schema:      createSchema,
			},
			Responses: map[int]ResponseSpec{
				201: {Description: "Paste created", ContentType: "application/json", Schema: pasteSchema},
				400: {Description: "Bad request", ContentType: "application/json", Schema: errSchema},
				429: {Description: "Rate limit exceeded", ContentType: "application/json", Schema: errSchema},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/pastes/{id}",
			Tag:         "pastes",
			Summary:     "Get paste",
			Description: "Returns paste metadata and content by ID. If the paste is a link (is_link=true), responds with a 302 redirect to the target URL instead of a JSON body — use the /raw endpoint to retrieve the target URL as text without redirecting.",
			Params: []Param{
				{Name: "id", In: "path", Required: true, Description: "Paste identifier", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "OK", ContentType: "application/json", Schema: pasteSchema},
				302: {Description: "Paste is a link; redirects to the target URL"},
				404: {Description: "Paste not found", ContentType: "application/json", Schema: errSchema},
			},
		},
		{
			Method:      "DELETE",
			Path:        apiBase + "/pastes/{id}",
			Tag:         "pastes",
			Summary:     "Delete paste",
			Description: "Deletes a paste. Requires `Authorization: Bearer <token>` header (the delete token returned at creation time).",
			Params: []Param{
				{Name: "id", In: "path", Required: true, Description: "Paste identifier", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				204: {Description: "Deleted"},
				401: {Description: "Unauthorized", ContentType: "application/json", Schema: errSchema},
				404: {Description: "Paste not found", ContentType: "application/json", Schema: errSchema},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/pastes/{id}/raw",
			Tag:         "pastes",
			Summary:     "Get raw paste content",
			Description: "Returns the raw paste text as plain text. For links (is_link=true), returns the target URL as plain text without redirecting.",
			Params: []Param{
				{Name: "id", In: "path", Required: true, Description: "Paste identifier", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Raw paste text", ContentType: "text/plain"},
				404: {Description: "Paste not found", ContentType: "text/plain"},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/server/healthz",
			Tag:         "server",
			Summary:     "Health check",
			Description: "Returns server health status and basic metrics.",
			Responses: map[int]ResponseSpec{
				200: {
					Description: "OK",
					ContentType: "application/json",
					Schema: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"status":  map[string]interface{}{"type": "string", "example": "ok"},
							"version": map[string]interface{}{"type": "string"},
							"uptime":  map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/server/version",
			Tag:         "server",
			Summary:     "Version info",
			Description: "Returns the server version, commit ID, and build date.",
			Responses: map[int]ResponseSpec{
				200: {
					Description: "OK",
					ContentType: "application/json",
					Schema: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"version":    map[string]interface{}{"type": "string"},
							"commit":     map[string]interface{}{"type": "string"},
							"build_date": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/server/swagger",
			Tag:         "server",
			Summary:     "OpenAPI specification",
			Description: "Returns the OpenAPI 3.0.3 JSON specification for this API.",
			Responses: map[int]ResponseSpec{
				200: {Description: "OpenAPI JSON spec", ContentType: "application/json"},
			},
		},
		{
			Method:      "POST",
			Path:        apiBase + "/server/graphql",
			Tag:         "server",
			Summary:     "GraphQL endpoint",
			Description: "Executes a GraphQL query or mutation. The interactive GraphiQL explorer is available at /server/docs/graphql.",
			Body: &BodySpec{
				Required:    true,
				ContentType: "application/json",
				Description: "GraphQL request with query and optional variables.",
				Schema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query":     map[string]interface{}{"type": "string"},
						"variables": map[string]interface{}{"type": "object"},
					},
					"required": []string{"query"},
				},
			},
			Responses: map[int]ResponseSpec{
				200: {
					Description: "GraphQL response",
					ContentType: "application/json",
					Schema: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"data":   map[string]interface{}{"type": "object"},
							"errors": map[string]interface{}{"type": "array"},
						},
					},
				},
			},
		},

		// ─── pastebin.com compatibility ─────────────────────────────────────────
		{
			Method:      "POST",
			Path:        "/api/api_post.php",
			Tag:         "compatibility",
			Summary:     "pastebin.com — create / list / delete",
			Description: "Wire-compatible with the pastebin.com API (https://pastebin.com/doc_api). Dispatches on `api_option`: `paste` (default) creates a paste; `list` returns recent public pastes as XML; `delete` removes a paste; `userdetails` returns a stub XML user record. `api_dev_key` and `api_user_key` are silently ignored.",
			Body: &BodySpec{
				Required:    true,
				ContentType: "application/x-www-form-urlencoded",
				Description: "pastebin.com form fields",
				Schema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"api_option":            map[string]interface{}{"type": "string", "enum": []string{"paste", "list", "delete", "userdetails"}, "default": "paste"},
						"api_paste_code":        map[string]interface{}{"type": "string", "description": "Paste content (required for paste)"},
						"api_paste_name":        map[string]interface{}{"type": "string", "description": "Optional title"},
						"api_paste_format":      map[string]interface{}{"type": "string", "description": "Language/format identifier"},
						"api_paste_private":     map[string]interface{}{"type": "string", "enum": []string{"0", "1", "2"}, "description": "0=public 1=unlisted 2=private (treated as unlisted)"},
						"api_paste_expire_date": map[string]interface{}{"type": "string", "description": "N/A 10M 1H 1D 1W 2W 1M 6M 1Y"},
						"api_dev_key":           map[string]interface{}{"type": "string", "description": "Ignored — any value accepted"},
					},
				},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste URL (plain text) or XML response depending on api_option", ContentType: "text/plain"},
				400: {Description: "Bad request", ContentType: "text/plain"},
			},
		},
		{
			Method:      "GET",
			Path:        "/api/api_raw.php",
			Tag:         "compatibility",
			Summary:     "pastebin.com — get raw paste",
			Description: "Wire-compatible with pastebin.com raw content endpoint. Returns the paste body as plain text.",
			Params: []Param{
				{Name: "i", In: "query", Required: true, Description: "Paste ID", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Raw paste content", ContentType: "text/plain"},
				404: {Description: "Paste not found", ContentType: "text/plain"},
			},
		},
		{
			Method:      "POST",
			Path:        "/api/api_login.php",
			Tag:         "compatibility",
			Summary:     "pastebin.com — login (stub)",
			Description: "Wire-compatible stub. Always returns ANONYMOUS. Required by tools that call this endpoint before creating pastes.",
			Responses: map[int]ResponseSpec{
				200: {Description: "Always returns ANONYMOUS", ContentType: "text/plain"},
			},
		},

		// ─── microbin compatibility ──────────────────────────────────────────────
		{
			Method:      "POST",
			Path:        apiBase + "/pasta",
			Tag:         "compatibility",
			Summary:     "microbin — create paste",
			Description: "Wire-compatible with the microbin API (https://github.com/szabodanika/microbin). Accepts form fields: `content`, `title`, `syntax`, `expiry`, `burn_after_reads`, `private`.",
			Body: &BodySpec{
				Required:    true,
				ContentType: "application/x-www-form-urlencoded",
				Description: "microbin paste fields",
				Schema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content":          map[string]interface{}{"type": "string"},
						"title":            map[string]interface{}{"type": "string"},
						"syntax":           map[string]interface{}{"type": "string"},
						"expiry":           map[string]interface{}{"type": "string"},
						"burn_after_reads": map[string]interface{}{"type": "integer"},
						"private":          map[string]interface{}{"type": "boolean"},
					},
				},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Created paste as JSON", ContentType: "application/json"},
				400: {Description: "Bad request", ContentType: "application/json"},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/pasta",
			Tag:         "compatibility",
			Summary:     "microbin — list pastes",
			Description: "Returns a list of pastes in microbin JSON format.",
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste list as JSON", ContentType: "application/json"},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/pasta/{id}",
			Tag:         "compatibility",
			Summary:     "microbin — get paste",
			Description: "Returns a single paste by ID in microbin JSON format.",
			Params: []Param{
				{Name: "id", In: "path", Required: true, Description: "Paste ID", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste as JSON", ContentType: "application/json"},
				404: {Description: "Not found", ContentType: "application/json"},
			},
		},
		{
			Method:      "DELETE",
			Path:        apiBase + "/pasta/{id}",
			Tag:         "compatibility",
			Summary:     "microbin — delete paste",
			Description: "Deletes a paste by ID.",
			Params: []Param{
				{Name: "id", In: "path", Required: true, Description: "Paste ID", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				204: {Description: "Deleted"},
				404: {Description: "Not found", ContentType: "application/json"},
			},
		},

		// ─── lenpaste compatibility ──────────────────────────────────────────────
		{
			Method:      "POST",
			Path:        "/api/new",
			Tag:         "compatibility",
			Summary:     "lenpaste — create paste",
			Description: "Wire-compatible with the lenpaste API (https://github.com/forksmgr/lcomrade-lenpaste). Also available at `/api/v1/new`. Accepts form fields: `text`, `title`, `syntax`, `ttl` (seconds), `oneUse`.",
			Body: &BodySpec{
				Required:    true,
				ContentType: "application/x-www-form-urlencoded",
				Description: "lenpaste paste fields",
				Schema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"text":   map[string]interface{}{"type": "string"},
						"title":  map[string]interface{}{"type": "string"},
						"syntax": map[string]interface{}{"type": "string"},
						"ttl":    map[string]interface{}{"type": "integer", "description": "Lifetime in seconds (0 = never)"},
						"oneUse": map[string]interface{}{"type": "boolean", "description": "Delete after first read"},
					},
				},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Created paste as JSON", ContentType: "application/json"},
				400: {Description: "Bad request", ContentType: "application/json"},
			},
		},
		{
			Method:      "GET",
			Path:        "/api/get",
			Tag:         "compatibility",
			Summary:     "lenpaste — get paste",
			Description: "Returns a paste by ID in lenpaste JSON format. Also available at `/api/v1/get`.",
			Params: []Param{
				{Name: "id", In: "query", Required: true, Description: "Paste ID", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste as JSON", ContentType: "application/json"},
				404: {Description: "Not found", ContentType: "application/json"},
			},
		},
		{
			Method:      "DELETE",
			Path:        "/api/remove",
			Tag:         "compatibility",
			Summary:     "lenpaste — delete paste",
			Description: "Deletes a paste. Also available at `/api/v1/remove` and via GET for legacy clients.",
			Params: []Param{
				{Name: "id", In: "query", Required: true, Description: "Paste ID", Schema: map[string]interface{}{"type": "string"}},
				{Name: "deleteToken", In: "query", Required: true, Description: "Delete token returned at creation", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				204: {Description: "Deleted"},
				403: {Description: "Invalid token", ContentType: "application/json"},
				404: {Description: "Not found", ContentType: "application/json"},
			},
		},
		{
			Method:      "GET",
			Path:        "/api/list",
			Tag:         "compatibility",
			Summary:     "lenpaste — list pastes",
			Description: "Returns a paginated list of pastes in lenpaste JSON format. Also available at `/api/v1/list`.",
			Params: []Param{
				{Name: "pageNum", In: "query", Required: false, Description: "Page number (default 1)", Schema: map[string]interface{}{"type": "integer"}},
				{Name: "pageLen", In: "query", Required: false, Description: "Items per page (default 20)", Schema: map[string]interface{}{"type": "integer"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste list as JSON", ContentType: "application/json"},
			},
		},
		{
			Method:      "GET",
			Path:        apiBase + "/getServerInfo",
			Tag:         "compatibility",
			Summary:     "lenpaste — server info",
			Description: "Returns server metadata in lenpaste JSON format: title, description, admin contact, limits, and syntax list.",
			Responses: map[int]ResponseSpec{
				200: {Description: "Server info as JSON", ContentType: "application/json"},
			},
		},

		// ─── stikked compatibility ───────────────────────────────────────────────
		{
			Method:      "POST",
			Path:        "/api/create",
			Tag:         "compatibility",
			Summary:     "stikked — create paste",
			Description: "Wire-compatible with the stikked API (https://github.com/claudehohl/Stikked). Returns the new paste URL as plain text.",
			Body: &BodySpec{
				Required:    true,
				ContentType: "application/x-www-form-urlencoded",
				Description: "stikked paste fields",
				Schema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"text":    map[string]interface{}{"type": "string"},
						"title":   map[string]interface{}{"type": "string"},
						"lang":    map[string]interface{}{"type": "string"},
						"private": map[string]interface{}{"type": "string", "enum": []string{"0", "1"}},
						"expire":  map[string]interface{}{"type": "integer", "description": "Seconds until expiry (0 = never)"},
					},
				},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste URL as plain text", ContentType: "text/plain"},
				400: {Description: "Bad request", ContentType: "text/plain"},
			},
		},
		{
			Method:      "GET",
			Path:        "/api/paste/{id}",
			Tag:         "compatibility",
			Summary:     "stikked — get paste as JSON",
			Description: "Returns paste metadata and content in stikked JSON format.",
			Params: []Param{
				{Name: "id", In: "path", Required: true, Description: "Paste ID", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste as JSON", ContentType: "application/json"},
				404: {Description: "Not found", ContentType: "application/json"},
			},
		},

		// ─── hastebin / haste-server compatibility ───────────────────────────────
		{
			Method:      "POST",
			Path:        "/documents",
			Tag:         "compatibility",
			Summary:     "hastebin — create document",
			Description: "Wire-compatible with the haste-server API (https://github.com/toptal/haste-server). Accepts raw body or `application/x-www-form-urlencoded` with `data` field. Returns `{\"key\":\"<id>\"}` as JSON.",
			Body: &BodySpec{
				Required:    true,
				ContentType: "text/plain",
				Description: "Raw paste content",
				Schema:      map[string]interface{}{"type": "string"},
			},
			Responses: map[int]ResponseSpec{
				200: {
					Description: "Key assigned to the document",
					ContentType: "application/json",
					Schema: map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{"key": map[string]interface{}{"type": "string"}},
					},
				},
				400: {Description: "Bad request", ContentType: "application/json"},
			},
		},
		{
			Method:      "GET",
			Path:        "/documents/{key}",
			Tag:         "compatibility",
			Summary:     "hastebin — get document",
			Description: "Returns document content in hastebin JSON format: `{\"key\":\"…\",\"data\":\"…\"}`.",
			Params: []Param{
				{Name: "key", In: "path", Required: true, Description: "Document key returned by POST /documents", Schema: map[string]interface{}{"type": "string"}},
			},
			Responses: map[int]ResponseSpec{
				200: {
					Description: "Document content",
					ContentType: "application/json",
					Schema: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"key":  map[string]interface{}{"type": "string"},
							"data": map[string]interface{}{"type": "string"},
						},
					},
				},
				404: {Description: "Not found", ContentType: "application/json"},
			},
		},

		// ─── dpaste compatibility ────────────────────────────────────────────────
		{
			Method:      "POST",
			Path:        "/api/",
			Tag:         "compatibility",
			Summary:     "dpaste — create paste",
			Description: "Wire-compatible with the dpaste API (https://github.com/bartTC/dpaste). Also available at `/api/v2/`. `format=url` returns a bare URL; `format=json` returns JSON with `url` and `content` fields. Accepts `lexer`, `expires` (seconds), and `title` fields.",
			Body: &BodySpec{
				Required:    true,
				ContentType: "application/x-www-form-urlencoded",
				Description: "dpaste paste fields",
				Schema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content": map[string]interface{}{"type": "string"},
						"lexer":   map[string]interface{}{"type": "string", "default": "text"},
						"expires": map[string]interface{}{"type": "integer", "description": "Lifetime in seconds"},
						"title":   map[string]interface{}{"type": "string"},
						"format":  map[string]interface{}{"type": "string", "enum": []string{"url", "json"}, "default": "url"},
					},
				},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste URL (plain text) or JSON depending on format param", ContentType: "text/plain"},
				400: {Description: "Bad request", ContentType: "text/plain"},
			},
		},

		// ─── curl-upload family (0x0.st / sprunge / ix.io) ──────────────────────
		{
			Method:      "POST",
			Path:        "/",
			Tag:         "compatibility",
			Summary:     "curl-upload — create paste (0x0.st / sprunge / ix.io)",
			Description: "Wire-compatible with the curl-upload one-liner family: 0x0.st (`-F file=@…`), sprunge (`-F sprunge=<-`), and ix.io (`-F f:1=<-`). Also accepts a raw request body. Returns the paste URL as plain text. When called with `Accept: application/json` or `?json`, returns `{\"ok\":true,\"url\":\"…\",\"delete_token\":\"…\"}`.",
			Body: &BodySpec{
				Required:    false,
				ContentType: "multipart/form-data",
				Description: "Upload via file field, sprunge field, f:1 field, or raw body",
				Schema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file":    map[string]interface{}{"type": "string", "format": "binary", "description": "0x0.st style — binary file field"},
						"sprunge": map[string]interface{}{"type": "string", "description": "sprunge.us style — text field"},
						"f:1":     map[string]interface{}{"type": "string", "description": "ix.io style — text field"},
					},
				},
			},
			Responses: map[int]ResponseSpec{
				200: {Description: "Paste URL as plain text (or JSON when requested)", ContentType: "text/plain"},
				400: {Description: "Empty content", ContentType: "text/plain"},
				413: {Description: "Payload too large", ContentType: "text/plain"},
				429: {Description: "Rate limit exceeded", ContentType: "text/plain"},
			},
		},
	}
	return mergeRouteAnnotations(routes, projectRouteAnnotations(apiVersion))
}

func projectRouteAnnotations(apiVersion string) []Route {
	apiBase := "/api/" + apiVersion
	definitions := []struct {
		method, path, summary, description string
	}{
		{"GET", "/api", "API discovery", "Returns the service name, version, and current API base URL."},
		{"GET", apiBase, "Versioned API discovery", "Returns service metadata for this API version."},
		{"GET", apiBase + "/server/auth/check", "Check token", "Identifies the presented operator or owner token without returning it or updating its activity."},
		{"GET", apiBase + "/server/contact", "Contact metadata", "Returns public contact-form metadata and available verification options."},
		{"POST", apiBase + "/server/contact", "Submit contact message", "Submits a contact message after configured anti-spam verification."},
		{"POST", apiBase + "/server/reports/{name}", "Submit report", "Submits a named security or abuse report and returns an acknowledgement."},
		{"GET", apiBase + "/server/about", "About page data", "Returns public project and operator-supplied about-page data."},
		{"GET", apiBase + "/server/dpo", "Data protection contact", "Returns the public data-protection contact details."},
		{"GET", apiBase + "/server/help", "Help content", "Returns public help content."},
		{"GET", apiBase + "/server/help/mcp", "MCP help", "Returns MCP connection and usage help."},
		{"GET", apiBase + "/server/privacy", "Privacy information", "Returns the public privacy statement."},
		{"GET", apiBase + "/server/security", "Security information", "Returns public security contact and disclosure information."},
		{"GET", apiBase + "/server/security/policy", "Security policy", "Returns the public security policy."},
		{"GET", apiBase + "/server/security/thanks", "Security acknowledgements", "Returns public security acknowledgements."},
		{"GET", apiBase + "/server/terms", "Terms of service", "Returns the public terms of service."},
		{"GET", apiBase + "/server/graphql", "GraphQL query", "Executes a read-only GraphQL query; POST is available for queries with variables."},
		{"POST", apiBase + "/server/mcp", "MCP endpoint", "Accepts a Streamable HTTP MCP request authenticated with a bearer token."},
		{"GET", apiBase + "/server/preferences", "Read preferences", "Returns the visitor's current preferences."},
		{"POST", apiBase + "/server/preferences", "Update preferences", "Applies a partial JSON update to visitor preferences."},
		{"GET", apiBase + "/server/preferences/sync", "Export preferences", "Returns preferences and cross-device import link data."},
		{"GET", apiBase + "/server/preferences/sync/import", "Import preferences", "Applies preferences encoded in the supplied import code."},
		{"GET", apiBase + "/server/preferences/tokens", "List owner tokens", "Returns the owner's token list with token values masked."},
		{"POST", apiBase + "/server/preferences/tokens", "Create linked token", "Creates a linked owner token."},
		{"POST", apiBase + "/server/preferences/tokens/primary/reveal", "Reveal primary token", "Reveals the primary owner token after the one-time confirmation flow."},
		{"POST", apiBase + "/server/preferences/tokens/primary/rotate", "Rotate primary token", "Rotates the primary owner token."},
		{"PATCH", apiBase + "/server/preferences/tokens/{id}", "Update linked token", "Updates linked-token metadata and permissions."},
		{"DELETE", apiBase + "/server/preferences/tokens/{id}", "Revoke linked token", "Revokes a linked owner token."},
		{"POST", apiBase + "/server/preferences/tokens/{id}/reveal", "Reveal linked token", "Reveals a linked owner token after the one-time confirmation flow."},
		{"POST", apiBase + "/server/preferences/tokens/{id}/rotate", "Rotate linked token", "Rotates a linked owner token."},
		{"GET", apiBase + "/schedulers", "List schedulers", "Returns registered background schedulers and their current status."},
		{"GET", apiBase + "/schedulers/{id}", "Get scheduler", "Returns configuration and current status for one scheduler."},
		{"GET", apiBase + "/schedulers/{id}/history", "Get scheduler history", "Returns recent execution history for one scheduler."},
		{"POST", apiBase + "/schedulers/{id}/run", "Run scheduler", "Triggers one scheduler run; requires the operator bearer token."},
		{"POST", apiBase + "/schedulers/{id}/enable", "Enable scheduler", "Enables one scheduler; requires the operator bearer token."},
		{"POST", apiBase + "/schedulers/{id}/disable", "Disable scheduler", "Disables one scheduler; requires the operator bearer token."},
		{"GET", "/api/autodiscover", "Client discovery", "Returns service URLs, optional overlay addresses, and client update metadata."},
		{"GET", "/api/swagger", "OpenAPI specification alias", "Returns the current version's OpenAPI JSON specification."},
		{"POST", "/api/graphql", "GraphQL alias", "Executes a GraphQL request against the current API version."},
		{"POST", "/api/mcp", "MCP alias", "Accepts an MCP request using the current API version and bearer-token authentication."},
		{"POST", "/mcp", "MCP root alias", "Accepts an MCP request using the current API version and bearer-token authentication."},
		{"GET", "/api/healthz", "Health check alias", "Returns the current server health document."},
		{"POST", "/api/api_post.php", "Pastebin-compatible create", "Creates a paste using the pastebin.com public API form protocol."},
		{"GET", "/api/api_raw.php", "Pastebin-compatible raw paste", "Returns raw paste content using the pastebin.com API query protocol."},
		{"POST", "/api/api_login.php", "Pastebin-compatible login stub", "Returns the anonymous login response for pastebin.com protocol clients."},
		{"POST", "/api/new", "Lenpaste-compatible create", "Creates a paste using the lenpaste-compatible form protocol."},
		{"GET", "/api/get", "Lenpaste-compatible retrieve", "Returns a paste using the lenpaste-compatible JSON response."},
		{"GET", "/api/remove", "Lenpaste-compatible remove", "Removes a paste using its compat delete token."},
		{"DELETE", "/api/remove", "Lenpaste-compatible remove", "Removes a paste using its compat delete token."},
		{"GET", "/api/list", "Lenpaste-compatible list", "Lists pastes using the lenpaste-compatible response format."},
		{"POST", apiBase + "/new", "Versioned lenpaste-compatible create", "Creates a paste using the versioned lenpaste-compatible form protocol."},
		{"GET", apiBase + "/get", "Versioned lenpaste-compatible retrieve", "Returns a paste using the versioned lenpaste-compatible JSON response."},
		{"GET", apiBase + "/getServerInfo", "Lenpaste-compatible server info", "Returns server metadata using the lenpaste-compatible response format."},
		{"POST", apiBase + "/pasta", "Microbin-compatible create", "Creates a paste using the microbin-compatible request protocol."},
		{"GET", apiBase + "/pasta", "Microbin-compatible list", "Lists pastes using the microbin-compatible response format."},
		{"GET", apiBase + "/pasta/{id}", "Microbin-compatible retrieve", "Returns a paste using the microbin-compatible response format."},
		{"DELETE", apiBase + "/pasta/{id}", "Microbin-compatible delete", "Deletes a paste using the microbin-compatible delete token."},
		{"POST", "/upload", "Microbin-compatible upload", "Creates a paste using the microbin-compatible upload form."},
		{"GET", "/upload/{id}", "Microbin-compatible view", "Renders a microbin-compatible paste page."},
		{"POST", "/api/create", "Stikked-compatible create", "Creates a paste using the stikked-compatible form protocol."},
		{"GET", "/api/paste/{id}", "Stikked-compatible retrieve", "Returns a paste using the stikked-compatible JSON response."},
		{"GET", "/view/{id}", "Stikked-compatible view", "Renders a stikked-compatible paste page."},
		{"GET", "/view/raw/{id}", "Stikked-compatible raw paste", "Returns the literal paste content."},
		{"POST", "/documents", "Hastebin-compatible create", "Creates a paste from the raw request body and returns its key."},
		{"GET", "/documents/{id}", "Hastebin-compatible retrieve", "Returns paste content and key using the hastebin-compatible JSON response."},
		{"GET", "/raw/{id}", "Hastebin-compatible raw paste", "Returns the literal paste content."},
		{"POST", "/api", "Dpaste-compatible create", "Creates a paste using the dpaste-compatible form protocol."},
		{"POST", "/api/", "Dpaste-compatible trailing-slash create", "Creates a paste using the dpaste-compatible form protocol."},
		{"POST", "/api/v2", "Dpaste v2-compatible create", "Creates a paste using the dpaste v2-compatible form protocol."},
		{"POST", "/api/v2/", "Dpaste v2-compatible trailing-slash create", "Creates a paste using the dpaste v2-compatible form protocol."},
		{"POST", "/", "Curl-upload create", "Creates a paste using multipart file, sprunge, ix.io, or raw-body input."},
	}
	routes := make([]Route, 0, len(definitions))
	for _, definition := range definitions {
		route := Route{
			Method:      definition.method,
			Path:        definition.path,
			Tag:         "compatibility",
			Summary:     definition.summary,
			Description: definition.description,
			Responses: map[int]ResponseSpec{
				200: {Description: "Successful response"},
				400: {Description: "Invalid request"},
				404: {Description: "Resource not found"},
			},
		}
		if strings.Contains(definition.path, "/server/") || strings.HasPrefix(definition.path, "/api/") || strings.HasPrefix(definition.path, apiBase) {
			route.Tag = "server"
		}
		for _, part := range strings.Split(definition.path, "/") {
			if len(part) > 2 && part[0] == '{' && part[len(part)-1] == '}' {
				name := part[1 : len(part)-1]
				route.Params = append(route.Params, Param{
					Name: name, In: "path", Required: true,
					Description: name + " path parameter",
					Schema:      map[string]interface{}{"type": "string"},
				})
			}
		}
		routes = append(routes, route)
	}
	return routes
}

func mergeRouteAnnotations(existing, generated []Route) []Route {
	seen := make(map[string]struct{}, len(existing))
	for _, route := range existing {
		seen[route.Method+" "+route.Path] = struct{}{}
	}
	for _, route := range generated {
		key := route.Method + " " + route.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, route)
	}
	return existing
}
