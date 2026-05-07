package render

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// embeddedTemplates contains the four base java-tron config files bundled
// into the binary (mainnet, nile, private, system-test). They are the
// source of truth when no external template directory is configured —
// release builds always work out of the box.
//
//go:embed templates/*.conf
var embeddedTemplates embed.FS

// LoadTemplate returns the raw HOCON template for the given network. When
// templateDir is non-empty and contains the matching file, the on-disk copy
// wins (useful for local development and tests). Otherwise we fall through
// to the embedded filesystem so a release binary always works without any
// co-located files.
func LoadTemplate(templateDir, network string) ([]byte, error) {
	fileName, ok := NetworkTemplate[network]
	if !ok {
		return nil, fmt.Errorf("unknown network %q", network)
	}

	if templateDir != "" {
		data, err := os.ReadFile(filepath.Join(templateDir, fileName))
		if err == nil {
			return data, nil
		}
		// Fall through — the embedded copy is authoritative when on-disk
		// lookup fails. Tests and CI shouldn't need to chdir to find templates.
	}

	data, err := embeddedTemplates.ReadFile("templates/" + fileName)
	if err != nil {
		return nil, fmt.Errorf("load embedded template %s: %w", fileName, err)
	}
	return data, nil
}
