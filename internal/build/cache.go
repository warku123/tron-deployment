package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// CacheDir returns the root of the build cache:
// `${TROND_STATE_DIR}/builds`. Created lazily by callers via
// EnsureCacheDirs.
func CacheDir() string {
	return filepath.Join(paths.BaseDir(), "builds")
}

// EnsureCacheDirs creates the cache subdirectories. Idempotent.
func EnsureCacheDirs() error {
	// Note: the gradle cache used to be a `gradle` bind-mounted
	// subdir here, but macOS Docker Desktop strips the exec bit on
	// files the container writes (breaks protoc + similar native
	// helpers). The runner now binds a docker named volume
	// (`trond-build-gradle-cache`) instead, so this dir list is
	// only for host-side outputs (artifacts, manifests, locks).
	// `worktrees` holds per-cache-key git worktree dirs created by
	// FR-026's `build.patches` path. Lifecycle is best-effort:
	// setupWorktree creates `<cacheDir>/worktrees/<key>/`,
	// removeWorktree tears it down after each build. SIGKILL between
	// the two leaves an orphan dir; Prune's worktree GC pass
	// reclaims those. Pre-creating the parent here means the first
	// patches build doesn't race against the parent mkdir.
	for _, sub := range []string{"out", "images", "manifest", "locks", "worktrees"} {
		p := filepath.Join(CacheDir(), sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	return nil
}

// manifestPath returns the manifest path for a cache key.
func manifestPath(key string) string {
	return filepath.Join(CacheDir(), "manifest", key+".json")
}

// Lookup checks whether a build matching key already exists and is
// still usable. Per FR-020 the manifest file's existence is necessary
// but not sufficient — we MUST also stat the artifact (jar or image
// metadata) that the manifest points at. A user who manually deleted
// a JAR shouldn't get a stale cache hit.
//
// ctx threads through the docker-inspect call for image artifacts;
// callers should pass the same signal-aware context they use for
// the rest of the build so a stuck docker daemon doesn't wedge the
// cache check.
func Lookup(ctx context.Context, key CacheKey) (*CacheHit, error) {
	mp := manifestPath(key.String())
	m, err := readManifest(mp)
	if errors.Is(err, os.ErrNotExist) {
		return &CacheHit{Hit: false}, nil
	}
	if err != nil {
		return nil, err
	}
	// Verify the referenced artifact actually exists on disk (FR-020).
	switch m.ArtifactKind {
	case "jar":
		if _, statErr := os.Stat(m.ArtifactPath); errors.Is(statErr, os.ErrNotExist) {
			// Drop the orphan manifest. Next build re-creates everything.
			_ = os.Remove(mp)
			return &CacheHit{Hit: false}, nil
		} else if statErr != nil {
			return nil, fmt.Errorf("stat cached artifact: %w", statErr)
		}
	case "image":
		// Image artifacts are tracked via images/<key>.json. We also
		// have to `docker image inspect` to confirm the local TAG
		// still resolves — a `docker rmi <tag>` (even one that left
		// the underlying image ID alive via other tags) makes
		// compose's `image: <tag>` field unresolvable. Tag-side
		// check is the authoritative one. (FR-020 cleanup branch.)
		meta, metaErr := readImageMetadata(key.String())
		if errors.Is(metaErr, os.ErrNotExist) {
			_ = os.Remove(mp)
			return &CacheHit{Hit: false}, nil
		}
		if metaErr != nil {
			return nil, fmt.Errorf("read image metadata: %w", metaErr)
		}
		if !imageTagExistsLocally(ctx, meta.Tag) {
			_ = os.Remove(mp)
			_ = os.Remove(filepath.Join(CacheDir(), "images", key.String()+".json"))
			return &CacheHit{Hit: false}, nil
		}
	}
	return &CacheHit{Hit: true, Manifest: m}, nil
}

// Save persists a manifest to manifest/<key>.json. Caller is
// responsible for atomicity vs. concurrent build callers — FR-015's
// flock already serializes around the same cache key.
func Save(m *Manifest) error {
	return writeManifest(manifestPath(m.CacheKey), m)
}
