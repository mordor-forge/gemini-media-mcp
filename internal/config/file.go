package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// mergeFile overlays the YAML (or JSON, which is valid YAML) file at path.
// It reports found=false when the file does not exist.
func (c *Config) mergeFile(path string) (found bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading config file %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return true, nil
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return true, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	probe := *c
	if err := strict.Decode(&probe); err != nil {
		// Unknown keys are tolerated (forward compatibility) but reported.
		if !strings.Contains(err.Error(), "not found in type") {
			return true, fmt.Errorf("parsing config file %s: %w", path, err)
		}
		c.warnf("config file %s: %s", path, strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n"))
	}
	if err := root.Decode(c); err != nil {
		return true, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	if c.APIKey != "" {
		c.warnf("config file %s contains apiKey; prefer the GEMINI_API_KEY environment variable or a secret manager", path)
	}

	if len(root.Content) > 0 && root.Content[0].Kind == yaml.MappingNode {
		m := root.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			c.setSource(m.Content[i].Value, "file")
		}
	}
	return true, nil
}
