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
	CacheKey           string        `json:"cache_key"`
	SourcePath         string        `json:"source_path"`
	SourceRevision     string        `json:"source_revision"`
	PatchHash          string        `json:"patch_hash,omitempty"`
	Dirty              bool          `json:"dirty"`
	BuilderImage       string        `json:"builder_image"`
	BuilderImageDigest string        `json:"builder_image_digest"`
	JDKVersion         string        `json:"jdk_version"`
	ArtifactKind       string        `json:"artifact_kind"`           // "jar" | "image"
	ArtifactPath       string        `json:"artifact_path,omitempty"` // for jar
	ImageTag           string        `json:"image_tag,omitempty"`     // for image
	ImageID            string        `json:"image_id,omitempty"`      // for image
	SHA256             string        `json:"sha256,omitempty"`        // for jar
	GradleTask         string        `json:"gradle_task"`
	GradleArgs         []string      `json:"gradle_args,omitempty"`
	Patches            []PatchRecord `json:"patches,omitempty"`  // FR-026; (basename, content sha256) per patch — see PatchRecord
	Builder            string        `json:"builder"`            // "docker" | "host"
	Platform           string        `json:"platform,omitempty"` // "linux/amd64" | "linux/arm64"
	DurationMs         int64         `json:"duration_ms"`
	CreatedAt          time.Time     `json:"created_at"`
}

// PatchRecord pairs a build.patches entry's basename with the
// sha256 of its file contents. Recorded in Manifest.Patches so:
//
//   - `trond build inspect <key>` shows what patches went into a
//     cached artifact (the basename is the human-readable label).
//   - An operator on a different machine can verify their local
//     patch files still match the content that produced the cache
//     entry (compare local sha256 vs Manifest record). This is the
//     authoritative fingerprint; absolute filesystem paths would
//     be misleading once patches are moved/renamed/pulled from a
//     shared TROND_STATE_DIR.
type PatchRecord struct {
	Name   string `json:"name"`   // filepath.Base of the patch path
	SHA256 string `json:"sha256"` // sha256 hex of the patch file contents
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
