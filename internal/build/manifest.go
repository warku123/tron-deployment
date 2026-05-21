package build

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Manifest is the JSON record persisted for every completed build.
// One file per cache key under `<cacheDir>/manifest/<key>.json`.
//
// Output schema: schemas/output/build.schema.json.
type Manifest struct {
	CacheKey           string    `json:"cache_key"`
	SourcePath         string    `json:"source_path"`
	SourceRevision     string    `json:"source_revision"`
	PatchHash          string    `json:"patch_hash,omitempty"`
	Dirty              bool      `json:"dirty"`
	BuilderImage       string    `json:"builder_image"`
	BuilderImageDigest string    `json:"builder_image_digest"`
	JDKVersion         string    `json:"jdk_version"`
	ArtifactKind       string    `json:"artifact_kind"`           // "jar" | "image"
	ArtifactPath       string    `json:"artifact_path,omitempty"` // for jar
	ImageTag           string    `json:"image_tag,omitempty"`     // for image
	ImageID            string    `json:"image_id,omitempty"`      // for image
	SHA256             string    `json:"sha256,omitempty"`        // for jar
	GradleTask         string    `json:"gradle_task"`
	GradleArgs         []string  `json:"gradle_args,omitempty"`
	Patches            []string  `json:"patches,omitempty"`  // FR-026; absolute paths declared in intent.build.patches
	Builder            string    `json:"builder"`            // "docker" | "host"
	Platform           string    `json:"platform,omitempty"` // "linux/amd64" | "linux/arm64"
	DurationMs         int64     `json:"duration_ms"`
	CreatedAt          time.Time `json:"created_at"`
}

// CacheHit is the body returned to callers when a previous build
// satisfies the request. The boolean is hoisted from inside Manifest
// so the caller's tooling (cmd/build.go, MCP tool, apply pipeline)
// can branch on it without reading the manifest first.
type CacheHit struct {
	Hit      bool      `json:"cache_hit"`
	Manifest *Manifest `json:"manifest,omitempty"`
}

// readManifest decodes a JSON manifest file. Returns os.ErrNotExist
// when the file is absent so callers can treat that as a miss.
func readManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", path, err)
	}
	return &m, nil
}

func writeManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write manifest %s: %w", path, err)
	}
	return nil
}
