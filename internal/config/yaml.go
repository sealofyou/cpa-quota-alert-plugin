package config

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxYAMLBytes = 1 << 20

var (
	ErrYAMLTooLarge    = errors.New("configuration YAML exceeds the size limit")
	ErrYAMLSyntax      = errors.New("configuration YAML is invalid")
	ErrYAMLMultipleDoc = errors.New("configuration YAML must contain exactly one document")
	ErrYAMLMapKey      = errors.New("configuration YAML mapping keys must be strings")
	ErrYAMLDuplicate   = errors.New("configuration YAML contains a duplicate mapping key")
)

// ParseYAML decodes the configuration supplied by CPA and applies the normal
// typed validation. Decoder errors intentionally exclude source text so a
// malformed document cannot echo secrets into plugin errors or logs.
func ParseYAML(data []byte, getenv Getenv) (Config, error) {
	raw, err := DecodeYAML(data)
	if err != nil {
		return Config{}, err
	}
	return Parse(raw, getenv)
}

func DecodeYAML(data []byte) (map[string]any, error) {
	if len(data) > MaxYAMLBytes {
		return nil, ErrYAMLTooLarge
	}
	if strings.TrimSpace(string(data)) == "" {
		return map[string]any{}, nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, ErrYAMLSyntax
	}
	if err := validateYAMLNode(&document, map[*yaml.Node]bool{}); err != nil {
		return nil, err
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return nil, ErrYAMLMultipleDoc
	} else if !errors.Is(err, io.EOF) {
		return nil, ErrYAMLSyntax
	}

	var raw map[string]any
	if err := document.Decode(&raw); err != nil {
		return nil, ErrYAMLSyntax
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}

func validateYAMLNode(node *yaml.Node, visiting map[*yaml.Node]bool) error {
	if node == nil {
		return nil
	}
	if visiting[node] {
		return ErrYAMLSyntax
	}
	visiting[node] = true
	defer delete(visiting, node)

	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateYAMLNode(child, visiting); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return ErrYAMLSyntax
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return ErrYAMLMapKey
			}
			if _, exists := seen[key.Value]; exists {
				return ErrYAMLDuplicate
			}
			seen[key.Value] = struct{}{}
			if err := validateYAMLNode(node.Content[i+1], visiting); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		return validateYAMLNode(node.Alias, visiting)
	case yaml.ScalarNode:
		return nil
	default:
		return ErrYAMLSyntax
	}
	return nil
}
