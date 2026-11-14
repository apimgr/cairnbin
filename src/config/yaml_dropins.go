package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type yamlConfigSource struct {
	path           string
	data           []byte
	dropIn         bool
	mode           fs.FileMode
	containsSecret bool
}

// decodeConfigFiles loads server.yml and yaml.d drop-ins into cfg. The returned
// map identifies the file that supplied each final scalar/list value.
func decodeConfigFiles(configPath string, cfg *Config) (map[string]string, error) {
	rootData, rootErr := os.ReadFile(configPath)
	if rootErr != nil && !os.IsNotExist(rootErr) {
		return nil, rootErr
	}
	if os.IsNotExist(rootErr) {
		rootData = nil
	}
	rootInfo, _ := os.Stat(configPath)
	sources := []yamlConfigSource{{path: configPath, data: rootData}}
	if rootInfo != nil {
		sources[0].mode = rootInfo.Mode()
	}
	dropIns, err := readYAMLDropIns(configPath)
	if err != nil {
		return nil, err
	}
	sources = append(sources, dropIns...)

	var merged *yaml.Node
	fieldSources := make(map[string]string)
	var overridden []struct{ key, path string }
	var decodeErrors []error
	for _, source := range sources {
		if len(source.data) == 0 {
			continue
		}
		data := migrateLegacyKeys(source.data)
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", source.path, err)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s:1: config document must be a mapping", source.path)
		}
		if err := validateConfigNode(&doc, "", source.path); err != nil {
			return nil, err
		}
		source.containsSecret = yamlNodeContainsSecret(&doc)
		if source.mode.Perm()&0o022 != 0 {
			return nil, fmt.Errorf("%s: config must not be group- or world-writable", source.path)
		}
		if source.containsSecret && source.mode.Perm() != 0o600 {
			return nil, fmt.Errorf("%s: config with secrets must have mode 0600", source.path)
		}
		probe := DefaultConfig()
		if err := decodeConfig(data, probe); err != nil {
			var typeErr *yaml.TypeError
			if !errors.As(err, &typeErr) {
				return nil, fmt.Errorf("%s: %w", source.path, err)
			}
			decodeErrors = append(decodeErrors, fmt.Errorf("%s: %w", source.path, err))
		}

		node := doc.Content[0]
		if merged == nil {
			merged = cloneYAMLNode(node)
			recordYAMLLeafSources(merged, "", source.path, fieldSources)
			continue
		}
		mergeYAMLMapping(merged, node, "", source.path, fieldSources, &overridden)
	}
	if merged != nil {
		encoded, err := yaml.Marshal(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{merged}})
		if err != nil {
			return nil, fmt.Errorf("encode merged config: %w", err)
		}
		if err := decodeConfig(encoded, cfg); err != nil {
			var typeErr *yaml.TypeError
			if !errors.As(err, &typeErr) {
				return nil, fmt.Errorf("decode merged config: %w", err)
			}
			if len(decodeErrors) == 0 {
				decodeErrors = append(decodeErrors, fmt.Errorf("merged config: %w", err))
			}
		}
	}
	for _, source := range dropIns {
		log.Printf("[config] loaded drop-in: %s", source.path)
	}
	if shouldLogConfigDebug() {
		for _, item := range overridden {
			log.Printf("[config] DEBUG: overridden key %s winner source %s", item.key, item.path)
		}
	}
	return fieldSources, errors.Join(decodeErrors...)
}

func readYAMLDropIns(configPath string) ([]yamlConfigSource, error) {
	dir := filepath.Join(filepath.Dir(configPath), "yaml.d")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read drop-in directory %s: %w", dir, err)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve drop-in directory %s: %w", dir, err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve drop-in directory %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []yamlConfigSource
	for _, entry := range entries {
		name := entry.Name()
		if ignoredConfigName(name) || !yamlConfigExtension(name) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("stat drop-in %s: %w", path, err)
		}
		resolved := path
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err = filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return nil, fmt.Errorf("resolve drop-in %s: %w", path, err)
			}
			rel, relErr := filepath.Rel(root, resolved)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			info, err = os.Stat(resolved)
			if err != nil {
				continue
			}
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read drop-in %s: %w", path, err)
		}
		out = append(out, yamlConfigSource{path: path, data: data, dropIn: true, mode: info.Mode()})
	}
	return out, nil
}

func ignoredConfigName(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".bak") || strings.HasSuffix(lower, ".orig")
}

func yamlConfigExtension(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
}

func validateConfigNode(node *yaml.Node, parent, source string) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		key := parent
		if key == "" {
			key = "<root>"
		}
		return fmt.Errorf("%s:%d: explicit null is not allowed for %s", source, node.Line, key)
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode, valueNode := node.Content[i], node.Content[i+1]
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag == "!!null" {
				return fmt.Errorf("%s:%d: config key under %s must be a string", source, keyNode.Line, parent)
			}
			key := keyNode.Value
			fullKey := key
			if parent != "" {
				fullKey = parent + "." + key
			}
			if seen[key] {
				return fmt.Errorf("%s:%d: duplicate config key %s", source, keyNode.Line, fullKey)
			}
			seen[key] = true
			if err := validateConfigNode(valueNode, fullKey, source); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := validateConfigNode(child, parent, source); err != nil {
			return err
		}
	}
	return nil
}

func yamlNodeContainsSecret(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.ToLower(strings.ReplaceAll(node.Content[i].Value, "-", "_"))
			if strings.Contains(key, "token") || strings.Contains(key, "password") ||
				strings.Contains(key, "secret") || strings.Contains(key, "credential") ||
				strings.Contains(key, "private_key") || strings.Contains(key, "api_key") {
				return true
			}
			if yamlNodeContainsSecret(node.Content[i+1]) {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if yamlNodeContainsSecret(child) {
			return true
		}
	}
	return false
}

func cloneYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	copyNode := *node
	copyNode.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copyNode.Content[i] = cloneYAMLNode(child)
	}
	if node.Alias != nil {
		copyNode.Alias = cloneYAMLNode(node.Alias)
	}
	return &copyNode
}

func mergeYAMLMapping(base, overlay *yaml.Node, parent, source string, sources map[string]string, overridden *[]struct{ key, path string }) {
	positions := make(map[string]int, len(base.Content)/2)
	for i := 0; i+1 < len(base.Content); i += 2 {
		positions[base.Content[i].Value] = i + 1
	}
	for i := 0; i+1 < len(overlay.Content); i += 2 {
		keyNode := cloneYAMLNode(overlay.Content[i])
		value := overlay.Content[i+1]
		key := keyNode.Value
		fullKey := key
		if parent != "" {
			fullKey = parent + "." + key
		}
		position, exists := positions[key]
		if !exists {
			base.Content = append(base.Content, keyNode, cloneYAMLNode(value))
			positions[key] = len(base.Content) - 1
			recordYAMLLeafSources(value, fullKey, source, sources)
			continue
		}
		old := base.Content[position]
		if old.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			mergeYAMLMapping(old, value, fullKey, source, sources, overridden)
			continue
		}
		*overridden = append(*overridden, struct{ key, path string }{fullKey, source})
		for path := range sources {
			if path == fullKey || strings.HasPrefix(path, fullKey+".") {
				delete(sources, path)
			}
		}
		base.Content[position] = cloneYAMLNode(value)
		recordYAMLLeafSources(value, fullKey, source, sources)
	}
}

func recordYAMLLeafSources(node *yaml.Node, parent, source string, sources map[string]string) {
	if node != nil && node.Kind == yaml.MappingNode && len(node.Content) > 0 {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			fullKey := key
			if parent != "" {
				fullKey = parent + "." + key
			}
			recordYAMLLeafSources(node.Content[i+1], fullKey, source, sources)
		}
		return
	}
	if parent != "" {
		sources[parent] = source
	}
}

// SourceFile reports which configuration file supplied the effective value at
// a dotted key path. An empty result means the value came from defaults/env.
func (c *Config) SourceFile(key string) string {
	if c == nil {
		return ""
	}
	return c.fieldSources[key]
}

// IsDropInOverride reports whether a key's value came from yaml.d rather than
// server.yml. It is used when generated values must not override a drop-in.
func (c *Config) IsDropInOverride(key string) bool {
	if c == nil {
		return false
	}
	source := c.fieldSources[key]
	return source != "" && source != c.configPath
}

func configFilesFingerprint(configPath string) string {
	var b strings.Builder
	appendFile := func(path string, includeContents bool) {
		b.WriteString(path)
		info, err := os.Lstat(path)
		if err != nil {
			b.WriteString("\x00" + err.Error() + "\x00")
			return
		}
		fmt.Fprintf(&b, "\x00%o:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		actual := path
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			b.WriteString("\x00" + target)
			if readErr != nil {
				return
			}
			actual, err = filepath.EvalSymlinks(path)
			if err != nil {
				b.WriteString("\x00" + err.Error())
				return
			}
			actualInfo, statErr := os.Stat(actual)
			if statErr != nil {
				b.WriteString("\x00" + statErr.Error())
				return
			}
			fmt.Fprintf(&b, "\x00%o:%d:%d", actualInfo.Mode(), actualInfo.Size(), actualInfo.ModTime().UnixNano())
		}
		if includeContents {
			data, readErr := os.ReadFile(actual)
			if readErr == nil {
				b.WriteString("\x00" + fmt.Sprintf("%x", sha256.Sum256(data)))
			} else {
				b.WriteString("\x00" + readErr.Error())
			}
		}
	}
	appendFile(configPath, true)
	dir := filepath.Join(filepath.Dir(configPath), "yaml.d")
	entries, err := os.ReadDir(dir)
	if err != nil {
		b.WriteString("\x00" + dir + ":" + err.Error())
	} else {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			appendFile(path, entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0)
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}
