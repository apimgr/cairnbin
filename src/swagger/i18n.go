package swagger

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

func localizeRoutes(routes []Route, lang string) []Route {
	localized := make([]Route, len(routes))
	copy(localized, routes)
	for idx := range localized {
		route := &localized[idx]
		route.Tag = localizeAnnotation(lang, route.Tag)
		route.Summary = localizeAnnotation(lang, route.Summary)
		route.Description = localizeAnnotation(lang, route.Description)
		if route.Params != nil {
			params := make([]Param, len(route.Params))
			copy(params, route.Params)
			for p := range params {
				params[p].Description = localizeAnnotation(lang, params[p].Description)
				params[p].Schema = localizeSchema(lang, params[p].Schema)
			}
			route.Params = params
		}
		if route.Body != nil {
			body := *route.Body
			body.Description = localizeAnnotation(lang, body.Description)
			body.Schema = localizeSchema(lang, body.Schema)
			route.Body = &body
		}
		if route.Responses != nil {
			responses := make(map[int]ResponseSpec, len(route.Responses))
			for code, response := range route.Responses {
				response.Description = localizeAnnotation(lang, response.Description)
				response.Schema = localizeSchema(lang, response.Schema)
				responses[code] = response
			}
			route.Responses = responses
		}
	}
	return localized
}

func localizeSchema(lang string, schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	localized := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		localized[key] = localizeSchemaValue(lang, key, value)
	}
	return localized
}

func localizeSchemaValue(lang, key string, value interface{}) interface{} {
	switch typed := value.(type) {
	case string:
		if key == "description" {
			return localizeAnnotation(lang, typed)
		}
	case map[string]interface{}:
		return localizeSchema(lang, typed)
	case []interface{}:
		localized := make([]interface{}, len(typed))
		for idx, item := range typed {
			localized[idx] = localizeSchemaValue(lang, "", item)
		}
		return localized
	}
	return value
}

func localizeAnnotation(lang, value string) string {
	if value == "" || lang == "" || lang == "en" {
		return value
	}
	key := annotationKey(value)
	translated := i18n.Translate(lang, key)
	if translated == key {
		return value
	}
	return translated
}

func annotationKey(value string) string {
	var slug strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			slug.WriteRune(r)
			lastUnderscore = false
		} else if !lastUnderscore && slug.Len() > 0 {
			slug.WriteByte('_')
			lastUnderscore = true
		}
		if slug.Len() >= 48 {
			break
		}
	}
	prefix := strings.Trim(slug.String(), "_")
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("swagger.%s_%x", prefix, digest[:4])
}
