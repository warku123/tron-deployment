package intent

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadWithOverlay loads a base intent and merges an overlay on top of it.
// The overlay can override any field; arrays (nodes) are replaced entirely.
func LoadWithOverlay(basePath, overlayPath string) (*Intent, error) {
	base, err := Load(basePath)
	if err != nil {
		return nil, fmt.Errorf("load base intent: %w", err)
	}

	overlayData, err := os.ReadFile(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("read overlay file: %w", err)
	}

	if err := mergeOverlay(base, overlayData); err != nil {
		return nil, fmt.Errorf("apply overlay: %w", err)
	}

	// Re-validate after merge
	if err := Validate(base); err != nil {
		return nil, fmt.Errorf("validate after overlay: %w", err)
	}

	ApplyDefaults(base)
	if err := ValidateJarRuntime(base); err != nil {
		return nil, err
	}
	return base, nil
}

// mergeOverlay applies overlay YAML on top of the base intent.
// Strategy: unmarshal overlay into a map, then re-marshal and unmarshal onto the base.
func mergeOverlay(base *Intent, overlayData []byte) error {
	// Parse overlay as a partial intent
	var overlay Intent
	dec := yaml.NewDecoder(bytes.NewReader(overlayData))
	dec.KnownFields(true)
	if err := dec.Decode(&overlay); err != nil {
		return fmt.Errorf("parse overlay YAML: %w", err)
	}

	// Simple field-level merge: override non-zero values
	if overlay.Name != "" {
		base.Name = overlay.Name
	}
	if overlay.Network != "" {
		base.Network = overlay.Network
	}
	if overlay.Target.Type != "" || overlay.Target.Host != "" || overlay.Target.Port != 0 || overlay.Target.User != "" || overlay.Target.IdentityFile != "" || overlay.Target.Runtime != "" || overlay.Target.AutoPorts {
		if overlay.Target.Type != "" {
			base.Target.Type = overlay.Target.Type
		}
		if overlay.Target.Host != "" {
			base.Target.Host = overlay.Target.Host
		}
		if overlay.Target.Port != 0 {
			base.Target.Port = overlay.Target.Port
		}
		if overlay.Target.User != "" {
			base.Target.User = overlay.Target.User
		}
		if overlay.Target.IdentityFile != "" {
			base.Target.IdentityFile = overlay.Target.IdentityFile
		}
		if overlay.Target.Runtime != "" {
			base.Target.Runtime = overlay.Target.Runtime
		}
		if overlay.Target.AutoPorts {
			base.Target.AutoPorts = true
		}
	}
	if len(overlay.Nodes) > 0 {
		base.Nodes = overlay.Nodes
	}

	return nil
}
