package config

import (
	"log"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// normalizeBools rewrites every scalar that feeds a bool field of t into the
// canonical true/false using ParseBool, so server.yml accepts the full PART 5
// truthy/falsy set (enable, oui, si, ...) and not only YAML's own booleans. A
// value ParseBool rejects is reported and turned into null, which leaves the
// field at its default instead of failing startup.
func normalizeBools(n *yaml.Node, t reflect.Type, path string) {
	if n == nil {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			normalizeBools(c, t, path)
		}
	case yaml.AliasNode:
		normalizeBools(n.Alias, t, path)
	case yaml.ScalarNode:
		if t.Kind() != reflect.Bool {
			return
		}
		if strings.TrimSpace(n.Value) == "" || n.Tag == "!!null" {
			return
		}
		b, err := ParseBool(n.Value, false)
		if err != nil {
			log.Printf("[config] WARNING: invalid boolean %q for %s, using default", n.Value, path)
			n.Tag, n.Value, n.Style = "!!null", "null", 0
			return
		}
		n.Tag, n.Style = "!!bool", 0
		n.Value = "false"
		if b {
			n.Value = "true"
		}
	case yaml.MappingNode:
		switch t.Kind() {
		case reflect.Struct:
			fields := yamlFieldTypes(t)
			for i := 0; i+1 < len(n.Content); i += 2 {
				key := n.Content[i].Value
				if ft, ok := fields[key]; ok {
					normalizeBools(n.Content[i+1], ft, join(path, key))
				}
			}
		case reflect.Map:
			for i := 0; i+1 < len(n.Content); i += 2 {
				normalizeBools(n.Content[i+1], t.Elem(), join(path, n.Content[i].Value))
			}
		}
	case yaml.SequenceNode:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for _, c := range n.Content {
				normalizeBools(c, t.Elem(), path)
			}
		}
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// yamlFieldTypes maps a struct's yaml keys to their field types, flattening
// `,inline` fields.
func yamlFieldTypes(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if strings.Contains(opts, "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for k, v := range yamlFieldTypes(ft) {
					out[k] = v
				}
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = f.Type
	}
	return out
}

// normalizeBoolDocument parses data, normalizes every bool-typed scalar for
// target's type, and returns the re-encoded document. Input that does not parse
// is returned unchanged so the regular decode reports the syntax error.
func normalizeBoolDocument(data []byte, target any) []byte {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind == 0 {
		return data
	}
	normalizeBools(&doc, reflect.TypeOf(target), "")
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return data
	}
	return out
}
