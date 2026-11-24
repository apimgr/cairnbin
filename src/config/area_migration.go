package config

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

var serverAreaFiles = map[string]string{
	"branding": "branding.yml", "seo": "branding.yml",
	"pages": "pages.yml", "contact": "contact.yml",
	"privacy": "privacy.yml", "compliance": "privacy.yml", "tracking": "privacy.yml",
	"security": "security.yml", "rate_limit": "security.yml", "geoip": "security.yml",
	"limits": "security.yml", "tokens": "security.yml", "data": "security.yml",
	"logs": "logs.yml", "metrics": "metrics.yml", "notifications": "notifications.yml",
	"tor": "tor.yml", "i2p": "i2p.yml",
	"maintenance": "maintenance.yml", "scheduler": "maintenance.yml", "update": "maintenance.yml",
	"auto_update": "maintenance.yml", "backup": "maintenance.yml", "restore": "maintenance.yml",
	"auth": "accounts.yml", "users": "accounts.yml", "orgs": "accounts.yml",
	"session": "accounts.yml", "admin": "accounts.yml", "admin_path": "accounts.yml",
	"cors": "web.yml", "csrf": "web.yml", "compression": "web.yml", "healthz": "web.yml",
	"i18n": "web.yml", "url_detection": "web.yml",
}

var serverNodeKeys = map[string]bool{
	"port": true, "address": true, "fqdn": true, "mode": true, "lang": true,
	"api_version": true, "baseurl": true, "user": true, "group": true,
	"pidfile": true, "daemonize": true, "debug": true, "database": true,
	"cache": true, "ssl": true, "trusted_proxies": true, "cluster": true,
}

const serverYAMLHeader = "# Settings of this node only (listen, identity, database, cache, ssl). Everything else lives in yaml.d/: branding.yml, web.yml, pages.yml, contact.yml, privacy.yml, security.yml, logs.yml, metrics.yml, notifications.yml, tor.yml, i2p.yml, maintenance.yml, accounts.yml, app.yml. Credentials live in secrets.yml.\n"

// migrateAreaFiles moves the sections of a legacy monolithic server.yml into
// their assigned yaml.d area files while preserving the merged configuration.
func migrateAreaFiles(configPath string) error {
	return migrateAreaFilesWithPrecedence(configPath, false)
}

func migrateAreaFilesWithPrecedence(configPath string, rootLast bool) error {
	originalData, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	data := migrateLegacyKeys(originalData)
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	dir := filepath.Dir(configPath)
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o200 == 0 {
		log.Printf("[config] area migration skipped: config directory %s is read-only", dir)
		return nil
	}
	root := document.Content[0]
	areas := make(map[string]*yaml.Node)
	rootChanged := false
	if !rootLast {
		rootChanged = moveAreaSections(root, areas, "")
	}
	yamlSources, err := readYAMLDropIns(configPath)
	if err != nil {
		return err
	}
	updatedDropIns := make(map[string][]byte)
	for _, source := range yamlSources {
		var dropDocument yaml.Node
		if err := yaml.Unmarshal(migrateLegacyKeys(source.data), &dropDocument); err != nil || len(dropDocument.Content) == 0 || dropDocument.Content[0].Kind != yaml.MappingNode {
			continue
		}
		if moveAreaSections(dropDocument.Content[0], areas, areaForFilename(filepath.Base(source.path))) {
			encoded, err := marshalYAML2(&dropDocument)
			if err != nil {
				return err
			}
			updatedDropIns[source.path] = encoded
		}
	}
	if rootLast {
		rootChanged = moveAreaSections(root, areas, "")
	}
	rootChanged = !bytes.Equal(data, originalData) || rootChanged
	if !rootChanged && len(updatedDropIns) == 0 {
		return nil
	}
	if len(areas) == 0 {
		if !bytes.Equal(data, originalData) {
			if err := writePrivateYAML(configPath+".bak", originalData, ".server.yml.bak-*"); err != nil {
				return fmt.Errorf("backup before legacy migration: %w", err)
			}
			data = append([]byte(serverYAMLHeader), data...)
			return writePrivateYAML(configPath, data, ".server.yml-*")
		}
		return nil
	}
	backupPath := configPath + ".bak"
	if err := writePrivateYAML(backupPath, originalData, ".server.yml.bak-*"); err != nil {
		return fmt.Errorf("backup before area migration: %w", err)
	}
	yamlDir := filepath.Join(dir, "yaml.d")
	if err := os.MkdirAll(yamlDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(yamlDir, 0o700); err != nil {
		return err
	}
	for name, moved := range areas {
		areaPath := filepath.Join(yamlDir, name)
		encoded, err := marshalYAML2(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{moved}})
		if err != nil {
			return err
		}
		if err := writePrivateYAML(areaPath, encoded, ".area-*.tmp"); err != nil {
			return err
		}
	}
	for path, updated := range updatedDropIns {
		if areas[filepath.Base(path)] != nil {
			updated = nil
		}
		if updated != nil {
			original, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := writePrivateYAML(path+".bak", original, ".dropin.yml.bak-*"); err != nil {
				return fmt.Errorf("backup before area migration of %s: %w", path, err)
			}
			if err := writePrivateYAML(path, updated, ".dropin-*.tmp"); err != nil {
				return err
			}
		}
	}
	if rootChanged {
		encoded, err := marshalYAML2(&document)
		if err != nil {
			return err
		}
		encoded = append([]byte(serverYAMLHeader), encoded...)
		if err := writePrivateYAML(configPath, encoded, ".server.yml-*"); err != nil {
			return err
		}
	}
	return nil
}

func moveAreaSections(root *yaml.Node, areas map[string]*yaml.Node, currentArea string) bool {
	changed := false
	serverIndex := mappingValueIndex(root, "server")
	if serverIndex >= 0 {
		serverNode := root.Content[serverIndex]
		for i := 0; i+1 < len(serverNode.Content); i += 2 {
			key := serverNode.Content[i].Value
			if serverNodeKeys[key] {
				continue
			}
			area := serverAreaFiles[key]
			if area == "" {
				continue
			}
			if area == currentArea {
				setYAMLSubtree(areaMapping(areas, area), []string{"server", key}, cloneYAMLNode(serverNode.Content[i+1]))
				continue
			}
			setYAMLSubtree(areaMapping(areas, area), []string{"server", key}, cloneYAMLNode(serverNode.Content[i+1]))
		}
		for i := len(serverNode.Content) - 2; i >= 0; i -= 2 {
			if !serverNodeKeys[serverNode.Content[i].Value] {
				serverNode.Content = append(serverNode.Content[:i], serverNode.Content[i+2:]...)
				changed = true
			}
		}
		if len(serverNode.Content) == 0 {
			deleteYAMLPath(root, []string{"server"})
		}
	}
	for i := len(root.Content) - 2; i >= 0; i -= 2 {
		key := root.Content[i].Value
		if key == "server" || key == "database" {
			continue
		}
		area := "app.yml"
		switch key {
		case "tor":
			area = "tor.yml"
		case "data":
			area = "security.yml"
		}
		if area == currentArea {
			setYAMLSubtree(areaMapping(areas, area), []string{key}, cloneYAMLNode(root.Content[i+1]))
			continue
		}
		setYAMLSubtree(areaMapping(areas, area), []string{key}, cloneYAMLNode(root.Content[i+1]))
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
		changed = true
	}
	return changed
}

func areaForFilename(name string) string {
	for _, candidate := range []string{"branding.yml", "web.yml", "pages.yml", "contact.yml", "privacy.yml", "security.yml", "logs.yml", "metrics.yml", "notifications.yml", "tor.yml", "i2p.yml", "maintenance.yml", "accounts.yml", "app.yml"} {
		if name == candidate {
			return candidate
		}
	}
	return ""
}

func mappingValueIndex(mapping *yaml.Node, key string) int {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i + 1
		}
	}
	return -1
}

func areaMapping(areas map[string]*yaml.Node, name string) *yaml.Node {
	if areas[name] == nil {
		areas[name] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	return areas[name]
}

func setYAMLSubtree(root *yaml.Node, path []string, value *yaml.Node) {
	if len(path) == 0 {
		return
	}
	if root.Kind != yaml.MappingNode {
		root.Kind = yaml.MappingNode
		root.Tag = "!!map"
	}
	index := mappingValueIndex(root, path[0])
	if len(path) == 1 {
		if index >= 0 {
			if root.Content[index].Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
				var overridden []struct{ key, path string }
				mergeYAMLMapping(root.Content[index], value, "", "", make(map[string]string), &overridden)
			} else {
				root.Content[index] = value
			}
		} else {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}, value)
		}
		return
	}
	if index < 0 {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		index = len(root.Content) - 1
	}
	setYAMLSubtree(root.Content[index], path[1:], value)
}
