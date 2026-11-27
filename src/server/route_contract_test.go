package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/go-chi/chi/v5"
)

type contractOperation struct {
	method string
	path   string
}

type contractPattern struct {
	method string
	path   string
}

func TestAPIRouteContract(t *testing.T) {
	projectRoot := routeContractProjectRoot(t)
	aiDocument := readRouteContractFile(t, filepath.Join(projectRoot, "AI.md"))
	ideaDocument := readRouteContractFile(t, filepath.Join(projectRoot, "IDEA.md"))
	apiVersion := config.DefaultConfig().Server.APIVersion

	templatePatterns, err := parseTemplateAPIContract(aiDocument, apiVersion)
	if err != nil {
		t.Fatal(err)
	}
	projectOperations, err := parseProjectAPIContract(ideaDocument, apiVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(projectOperations) == 0 {
		t.Fatal("IDEA.md ### API routes contains no HTTP operations")
	}

	cfg := config.DefaultConfig()
	server := New(&stubDB{}, cfg, nil, "test", "test", "test", t.TempDir(), t.TempDir())
	registeredOperations := collectRegisteredAPIContract(t, server.router, templatePatterns, projectOperations, apiVersion)
	specOperations := collectOpenAPIContract(t, server, templatePatterns, projectOperations, apiVersion)

	for _, operation := range projectOperations {
		if !containsOperation(registeredOperations, operation) {
			t.Errorf("IDEA.md documents %s %s, but the router does not register it", operation.method, operation.path)
		}
		if requiresOpenAPIDocumentation(operation) && !containsOperation(specOperations, operation) {
			t.Errorf("IDEA.md documents %s %s, but OpenAPI does not document it", operation.method, operation.path)
		}
	}

	for _, pattern := range templatePatterns {
		if pattern.method == "*" || strings.Contains(pattern.path, "*") || strings.Contains(pattern.path, "[") {
			if !containsPattern(registeredOperations, pattern) {
				t.Errorf("AI.md template route %s %s has no registered router route", pattern.method, pattern.path)
			}
			if requiresOpenAPIDocumentation(contractOperation(pattern)) && !containsPattern(specOperations, pattern) {
				t.Errorf("AI.md template route %s %s is missing from OpenAPI", pattern.method, pattern.path)
			}
			continue
		}
		operation := contractOperation(pattern)
		if !containsOperation(registeredOperations, operation) {
			t.Errorf("AI.md template route %s %s has no registered router operation", operation.method, operation.path)
		}
		if requiresOpenAPIDocumentation(operation) && !containsOperation(specOperations, operation) {
			t.Errorf("AI.md template route %s %s is missing from OpenAPI", operation.method, operation.path)
		}
	}

	for _, operation := range registeredOperations {
		if !coveredByContract(operation, templatePatterns, projectOperations) {
			t.Errorf("router registers undocumented API/compat operation %s %s", operation.method, operation.path)
		}
		if !requiresOpenAPIDocumentation(operation) {
			continue
		}
		if operation.method == "*" {
			if !containsPath(specOperations, operation.path) {
				t.Errorf("router registers %s for all methods, but OpenAPI does not document the path", operation.path)
			}
			continue
		}
		if !containsOperation(specOperations, operation) {
			t.Errorf("router registers %s %s, but OpenAPI does not document it", operation.method, operation.path)
		}
	}
	for _, operation := range specOperations {
		if !coveredByContract(operation, templatePatterns, projectOperations) {
			t.Errorf("OpenAPI documents operation absent from the AI.md/IDEA.md route contract: %s %s", operation.method, operation.path)
		}
		if !containsOperation(registeredOperations, operation) {
			t.Errorf("OpenAPI documents %s %s, but the router does not register it", operation.method, operation.path)
		}
	}
}

func requiresOpenAPIDocumentation(operation contractOperation) bool {
	return !strings.Contains(operation.path, "/metrics") &&
		!strings.HasPrefix(operation.path, "/server/tor/") &&
		!strings.HasPrefix(operation.path, "/debug/")
}

func routeContractProjectRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve route contract test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
}

func readRouteContractFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read route contract source %s: %v", path, err)
	}
	return string(contents)
}

func parseTemplateAPIContract(document, apiVersion string) ([]contractPattern, error) {
	const heading = "## Root-Level Endpoints"
	start := strings.Index(document, heading)
	if start < 0 {
		return nil, fmt.Errorf("AI.md is missing %q", heading)
	}
	section := document[start+len(heading):]
	if end := strings.Index(section, "\n**NOTE:**"); end >= 0 {
		section = section[:end]
	}
	var patterns []contractPattern
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := markdownTableCells(line)
		if len(cells) < 3 || cells[0] == "Endpoint" || strings.HasPrefix(cells[0], "-") {
			continue
		}
		path := trimMarkdownCode(cells[0])
		if !isTemplateAPIPath(path) {
			continue
		}
		for _, method := range strings.Split(cells[1], ",") {
			method = strings.ToUpper(strings.TrimSpace(method))
			if method == "" {
				continue
			}
			if method == "ALL" {
				method = "*"
			}
			patterns = append(patterns, contractPattern{method: method, path: normalizeContractPath(path, apiVersion)})
		}
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("AI.md %s contains no API routes", heading)
	}
	return patterns, nil
}

func parseProjectAPIContract(document, apiVersion string) ([]contractOperation, error) {
	const heading = "### API routes"
	start := strings.Index(document, heading)
	if start < 0 {
		return nil, fmt.Errorf("IDEA.md is missing required %q inventory", heading)
	}
	section := document[start+len(heading):]
	if end := strings.Index(section, "\n### "); end >= 0 {
		section = section[:end]
	}
	operations := make(map[contractOperation]struct{})
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := markdownTableCells(line)
		if len(cells) < 4 || cells[0] == "Method" || strings.HasPrefix(cells[0], "-") {
			continue
		}
		path := trimMarkdownCode(cells[1])
		if strings.TrimSpace(cells[2]) == "" || strings.TrimSpace(cells[3]) == "" {
			return nil, fmt.Errorf("IDEA.md route row must include auth and purpose/request/response details: %s", line)
		}
		if strings.EqualFold(cells[0], "TCP") {
			continue
		}
		for _, method := range strings.Split(cells[0], ",") {
			method = strings.ToUpper(strings.TrimSpace(method))
			if !isHTTPMethod(method) {
				return nil, fmt.Errorf("IDEA.md has invalid API route method %q in %s", method, line)
			}
			operations[contractOperation{method: method, path: normalizeContractPath(path, apiVersion)}] = struct{}{}
		}
	}
	result := make([]contractOperation, 0, len(operations))
	for operation := range operations {
		result = append(result, operation)
	}
	sortContractOperations(result)
	return result, nil
}

func markdownTableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func trimMarkdownCode(value string) string {
	return strings.Trim(strings.TrimSpace(value), "`")
}

func isHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func isTemplateAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api") || path == "/mcp" || strings.HasPrefix(path, "/server/metrics")
}

func normalizeContractPath(path, apiVersion string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "`")
	path = strings.ReplaceAll(path, "/api/"+apiVersion, "/api/{api_version}")
	path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllStringFunc(path, func(parameter string) string {
		if parameter == "{api_version}" {
			return parameter
		}
		return "{param}"
	})
	path = regexp.MustCompile(`/+$`).ReplaceAllString(path, "")
	if path == "" {
		return "/"
	}
	return path
}

func collectRegisteredAPIContract(t *testing.T, router chi.Router, templates []contractPattern, projectOperations []contractOperation, apiVersion string) []contractOperation {
	t.Helper()
	operations := make(map[contractOperation]struct{})
	if err := chi.Walk(router, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		method = strings.ToUpper(method)
		operation := contractOperation{method: method, path: normalizeContractPath(path, apiVersion)}
		if !coveredByContract(operation, templates, projectOperations) {
			return nil
		}
		operations[operation] = struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("walk registered routes: %v", err)
	}
	result := make([]contractOperation, 0, len(operations))
	for operation := range operations {
		result = append(result, operation)
	}
	sortContractOperations(result)
	return result
}

func collectOpenAPIContract(t *testing.T, server *Server, templates []contractPattern, projectOperations []contractOperation, apiVersion string) []contractOperation {
	t.Helper()
	response := httptest.NewRecorder()
	server.swaggerHandler.ServeSpec(response, httptest.NewRequest(http.MethodGet, "/api/"+apiVersion+"/server/swagger", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI response status = %d, want %d", response.Code, http.StatusOK)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}
	operations := make(map[contractOperation]struct{})
	for path, methods := range document.Paths {
		path = normalizeContractPath(path, apiVersion)
		for method := range methods {
			method = strings.ToUpper(method)
			if !isHTTPMethod(method) {
				continue
			}
			operation := contractOperation{method: method, path: path}
			if coveredByContract(operation, templates, projectOperations) {
				operations[operation] = struct{}{}
			}
		}
	}
	result := make([]contractOperation, 0, len(operations))
	for operation := range operations {
		result = append(result, operation)
	}
	sortContractOperations(result)
	return result
}

func containsOperation(operations []contractOperation, want contractOperation) bool {
	for _, operation := range operations {
		if contractPathMatches(operation.path, want.path) && (operation.method == want.method || operation.method == "*") {
			return true
		}
	}
	return false
}

func containsPath(operations []contractOperation, path string) bool {
	for _, operation := range operations {
		if operation.path == path {
			return true
		}
	}
	return false
}

func containsPattern(operations []contractOperation, pattern contractPattern) bool {
	for _, operation := range operations {
		if pattern.method != "*" && operation.method != pattern.method {
			continue
		}
		if contractPathMatches(pattern.path, operation.path) {
			return true
		}
	}
	return false
}

func coveredByContract(operation contractOperation, templates []contractPattern, project []contractOperation) bool {
	if containsOperation(project, operation) {
		return true
	}
	for _, pattern := range templates {
		if pattern.method != "*" && pattern.method != operation.method {
			continue
		}
		if contractPathMatches(pattern.path, operation.path) {
			return true
		}
	}
	return false
}

func contractPathMatches(pattern, path string) bool {
	if pattern == path {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	if open := strings.Index(pattern, "["); open >= 0 {
		close := strings.Index(pattern[open:], "]")
		if close >= 0 {
			optional := pattern[open+1 : open+close]
			base := pattern[:open] + pattern[open+close+1:]
			if path == base || path == strings.TrimSuffix(base, "/")+optional {
				return true
			}
		}
	}
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for index, part := range patternParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if pathParts[index] == "" {
				return false
			}
			continue
		}
		if part != pathParts[index] {
			return false
		}
	}
	return true
}

func sortContractOperations(operations []contractOperation) {
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].path == operations[j].path {
			return operations[i].method < operations[j].method
		}
		return operations[i].path < operations[j].path
	})
}
