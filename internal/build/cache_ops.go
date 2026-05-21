package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is the unified view of a single cached build for `trond
// build list / inspect / prune`. It embeds the persisted manifest
// and decorates it with on-disk state (artifact size, orphan
// detection) the caller would otherwise have to recompute. The
// embedded Manifest's JSON tags flatten cleanly into Entry's JSON
// output — the schema is just "manifest fields plus size_bytes plus
// orphaned".
type Entry struct {
	*Manifest

	// SizeBytes is the on-disk footprint trond's prune would reclaim.
	// For JARs it's the artifact file size; for images it's the
	// reported image size from `docker image inspect` (0 if docker
	// unreachable — we still return the entry, just without size).
	SizeBytes int64 `json:"size_bytes"`

	// Orphaned is true when the artifact a manifest points at is
	// gone (JAR file deleted from <cacheDir>/out, image rm'd from
	// the docker daemon). Lookup() drops orphans transparently on a
	// cache miss; list/inspect/prune surface them so operators can
	// see the gap explicitly.
	Orphaned bool `json:"orphaned"`
}

// listOptions is the internal filter knob set; the public `ListEntries`
// keeps a small signature, and the cobra layer maps user flags onto
// these fields via the options-functions pattern.
type listOptions struct {
	includeOrphans bool
}

// ListOption mutates listOptions.
type ListOption func(*listOptions)

// IncludeOrphans includes entries whose underlying artifact is gone.
// Default behaviour skips them — `trond build list` is a "what can I
// actually use" view; orphans only matter to `prune`.
func IncludeOrphans() ListOption {
	return func(o *listOptions) { o.includeOrphans = true }
}

// ListEntries walks the manifest directory and returns one Entry per
// persisted cache key, with each entry's artifact size + orphan state
// computed. Sorted newest-first by CreatedAt; the cobra layer re-sorts
// if the user asked for a different ordering.
//
// Errors only surface for unreadable directories — individual
// malformed manifest files are skipped with a stderr warning so one
// corrupt entry doesn't black out the rest of the cache.
func ListEntries(ctx context.Context, opts ...ListOption) ([]*Entry, error) {
	cfg := listOptions{}
	for _, o := range opts {
		o(&cfg)
	}

	manifestDir := filepath.Join(CacheDir(), "manifest")
	dirents, err := os.ReadDir(manifestDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No cache yet — empty list is the right answer, not an error.
			return nil, nil
		}
		return nil, fmt.Errorf("read manifest dir %s: %w", manifestDir, err)
	}

	entries := make([]*Entry, 0, len(dirents))
	for _, d := range dirents {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		mp := filepath.Join(manifestDir, d.Name())
		m, err := readManifest(mp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skip unreadable manifest %s: %v\n", mp, err)
			continue
		}
		e := decorateEntry(ctx, m)
		if e.Orphaned && !cfg.includeOrphans {
			continue
		}
		entries = append(entries, e)
	}

	// Newest-first default; deterministic on tied timestamps so test
	// output doesn't flake.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].CacheKey < entries[j].CacheKey
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	return entries, nil
}

// decorateEntry computes the per-artifact-kind size + orphan state.
// Image inspection touches docker; jar inspection is a single os.Stat.
func decorateEntry(ctx context.Context, m *Manifest) *Entry {
	e := &Entry{Manifest: m}
	switch m.ArtifactKind {
	case "jar":
		info, err := os.Stat(m.ArtifactPath)
		if errors.Is(err, os.ErrNotExist) {
			e.Orphaned = true
			return e
		}
		if err != nil {
			// Stat failed for a reason other than missing-file (perms,
			// I/O). Surface as 0 size + not-orphaned so prune doesn't
			// accidentally delete an entry whose artifact is just
			// temporarily unreadable.
			return e
		}
		e.SizeBytes = info.Size()
	case "image":
		size, ok := dockerImageSize(ctx, m.ImageTag)
		if !ok {
			e.Orphaned = true
			return e
		}
		e.SizeBytes = size
	}
	return e
}

// dockerImageSize calls `docker image inspect --format='{{.Size}}'`
// for the given tag. Returns (0, false) if docker is unreachable OR
// the tag doesn't resolve — the caller treats both as "orphaned"
// from trond's cache-tracking perspective.
func dockerImageSize(ctx context.Context, tag string) (int64, bool) {
	if tag == "" {
		return 0, false
	}
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect",
		"--format={{.Size}}", tag)
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	var size int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &size); err != nil {
		return 0, false
	}
	return size, true
}

// Sentinel errors so the cobra layer can map to exit codes /
// structured-error codes without string-matching.
var (
	ErrNoMatch         = errors.New("no cache entry matches the given key")
	ErrAmbiguousPrefix = errors.New("ambiguous cache-key prefix matches multiple entries")
)

// InspectEntry finds a single entry by full cache-key OR by an
// unambiguous prefix. Prefix lookup is for ergonomics — the canonical
// cache key is long (12 git chars + 8 digest chars + optional dirty/
// extra suffixes), and operators referring to it from terminal output
// shouldn't have to type the whole thing.
//
// Returns ErrNoMatch if no entry matches and ErrAmbiguousPrefix when
// the prefix matches >1 entry (cobra layer maps both to friendly
// CLI errors with the candidate list).
func InspectEntry(ctx context.Context, keyOrPrefix string) (*Entry, error) {
	if keyOrPrefix == "" {
		return nil, fmt.Errorf("cache key is required")
	}

	// Full-key fast path: try manifest/<key>.json directly. Saves
	// reading every other manifest just to find one.
	if m, err := readManifest(manifestPath(keyOrPrefix)); err == nil {
		return decorateEntry(ctx, m), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Prefix path: scan and collect matches. Include orphans because
	// inspect's whole job is "tell me about this specific entry,
	// even if it's busted".
	all, err := ListEntries(ctx, IncludeOrphans())
	if err != nil {
		return nil, err
	}
	var matches []*Entry
	for _, e := range all {
		if strings.HasPrefix(e.CacheKey, keyOrPrefix) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrNoMatch
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%w (%d candidates: %s)",
			ErrAmbiguousPrefix, len(matches), candidateList(matches))
	}
}

// candidateList builds a short human-readable list of cache keys
// for inclusion in the ambiguous-prefix error message.
func candidateList(matches []*Entry) string {
	keys := make([]string, 0, len(matches))
	for _, e := range matches {
		keys = append(keys, e.CacheKey)
	}
	return strings.Join(keys, ", ")
}

// PruneOptions configures the prune policy. Filters compose with
// AND semantics — an entry is pruned only if it matches every active
// filter — so combining e.g. --older-than 7d --keep-last 5 yields
// "remove things older than 7 days BUT always keep the 5 newest".
//
// All: shortcut for "remove everything" (overrides the per-filter
// fields; requires Confirm at the cobra layer).
//
// OrphanOnly: restrict to entries whose artifact is gone. Safe to run
// without other filters — it never touches an entry whose artifact
// is intact.
//
// DryRun: compute Plan, set Removed to nil, do not touch the cache.
type PruneOptions struct {
	All        bool
	OlderThan  time.Duration
	KeepLast   int
	OrphanOnly bool
	DryRun     bool
}

// PruneResult is the structured output the CLI/MCP layer renders.
// Plan is what WOULD be removed; Removed is what was actually
// removed (nil when DryRun). FreedBytes is the sum of SizeBytes for
// successfully-removed entries (or, on DryRun, the sum over Plan).
// Schema contract: schemas/output/build-prune.schema.json describes
// freed_bytes as "Total bytes reclaimed"; the post-loop recomputation
// below honors that even when one entry's removal partially failed.
type PruneResult struct {
	Plan       []*Entry `json:"plan"`
	Removed    []*Entry `json:"removed,omitempty"`
	FreedBytes int64    `json:"freed_bytes"`
	DryRun     bool     `json:"dry_run"`
}

// Prune evaluates the cache against opts, builds a deletion plan, and
// (unless DryRun) executes it under per-entry flocks so concurrent
// `trond build` runs against the same cache key cannot interleave
// with our manifest/artifact deletion. Image artifacts also get a
// best-effort `docker rmi <tag>` so the docker storage actually
// reclaims layers; failures there don't abort the prune (the trond
// cache files still get cleaned).
//
// Concurrency invariants:
//
//   - The flock per cache key matches the one builder.go acquires in
//     Run() (AcquireCacheLock). A concurrent build of the same key
//     either finishes first (we then prune the produced artifact,
//     unsurprising) or blocks until we release (the build then sees
//     no manifest and rebuilds, also unsurprising). No race window
//     where Prune deletes a half-written manifest mid-build.
//   - Two concurrent Prune invocations on the same entry race only
//     on the lock acquisition; the second sees the manifest already
//     gone in removeEntry and treats it as best-effort (the
//     errors.Is(..., os.ErrNotExist) branch).
func Prune(ctx context.Context, opts PruneOptions) (*PruneResult, error) {
	all, err := ListEntries(ctx, IncludeOrphans())
	if err != nil {
		return nil, err
	}
	plan := selectForPrune(all, opts, time.Now())

	result := &PruneResult{
		Plan:   plan,
		DryRun: opts.DryRun,
	}
	if opts.DryRun {
		// Dry-run reports what WOULD be freed — the plan's full
		// size, since nothing is removed.
		for _, e := range plan {
			result.FreedBytes += e.SizeBytes
		}
		return result, nil
	}

	for _, e := range plan {
		// Honor cancellation between entries — each iteration may
		// include a `docker image rm` (image artifacts) that can
		// run for tens of seconds; without this check, Ctrl+C only
		// takes effect when the next docker call returns. Plan
		// remains populated so the caller sees what WOULD have
		// been removed at the cancellation point.
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// FR-015 lock — same key the builder grabs in Run(). We use
		// the non-blocking try-variant: if a build holds this key
		// right now, prune skips the entry rather than waiting (a
		// background prune should NEVER stall an interactive build).
		release, ok, lockErr := TryAcquireCacheLock(CacheDir(), e.CacheKey)
		if lockErr != nil {
			fmt.Fprintf(os.Stderr,
				"warning: prune skip %s — lock open failed: %v\n",
				e.CacheKey, lockErr)
			continue
		}
		if !ok {
			fmt.Fprintf(os.Stderr,
				"info: prune skip %s — build in progress for this key\n",
				e.CacheKey)
			continue
		}
		err := removeEntry(ctx, e)
		release()
		if err != nil {
			// Don't abort the whole prune — one wedged docker rmi
			// shouldn't block the rest of the cleanup. Surface to
			// stderr; result.Removed + FreedBytes reflect only what
			// actually succeeded so the JSON contract holds even
			// after partial failures.
			fmt.Fprintf(os.Stderr,
				"warning: prune partial failure for %s: %v\n",
				e.CacheKey, err)
			continue
		}
		result.Removed = append(result.Removed, e)
		result.FreedBytes += e.SizeBytes
	}

	// FR-026 follow-up: GC orphan git worktree directories under
	// $cacheDir/worktrees/. A worktree is "orphan" when:
	//   - Its name (basename of the dir) doesn't match any current
	//     manifest's cache key, AND
	//   - The cache-key lock isn't currently held (we use the same
	//     TryAcquireCacheLock as the manifest loop, so an in-flight
	//     build's worktree is never touched).
	//
	// Orphans are produced by:
	//   - SIGKILL between setupWorktree() and the deferred cleanup.
	//   - Process panics that skip defers (Go does NOT run defers on
	//     fatal signals or runtime.Goexit-from-panic in some cases).
	//   - Race conditions during git's own worktree bookkeeping.
	//
	// We always run this GC pass after the manifest loop so a single
	// `trond build prune` reclaims both manifests AND worktree
	// orphans — operators don't need to know about a second cleanup
	// surface. The GC is best-effort: errors surface to stderr but
	// don't fail the prune.
	gcOrphanWorktrees(ctx, all, result)
	return result, nil
}

// gcOrphanWorktrees walks $cacheDir/worktrees/ and removes dirs
// whose cache-key basename doesn't match any current manifest and
// isn't currently locked. The freed bytes are summed into
// result.FreedBytes so the prune output is honest about ALL the
// disk it reclaimed.
func gcOrphanWorktrees(ctx context.Context, allEntries []*Entry, result *PruneResult) {
	worktreesDir := filepath.Join(CacheDir(), "worktrees")
	dirents, err := os.ReadDir(worktreesDir)
	if err != nil {
		// Missing dir is fine (no worktrees ever created); other
		// errors are surfaced but don't abort the prune.
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "warning: worktree GC scan %s: %v\n",
				worktreesDir, err)
		}
		return
	}

	// Build a set of currently-valid cache keys from the manifest
	// list. A worktree whose name matches one of these is potentially
	// in use by an active build → skip via the flock check.
	validKeys := make(map[string]bool, len(allEntries))
	for _, e := range allEntries {
		validKeys[e.CacheKey] = true
	}

	for _, d := range dirents {
		if err := ctx.Err(); err != nil {
			return
		}
		if !d.IsDir() {
			continue
		}
		key := d.Name()
		// Don't GC a worktree whose key still has a live manifest —
		// it might be in legitimate use. If the manifest gets pruned
		// in this same Run, the next prune call cleans the
		// now-orphan worktree.
		if validKeys[key] {
			continue
		}
		// Lock check: only GC if nobody else holds the key. A build
		// in flight (between setupWorktree and Manifest.Save) is the
		// realistic concurrent case here.
		release, ok, lockErr := TryAcquireCacheLock(CacheDir(), key)
		if lockErr != nil || !ok {
			continue
		}
		path := filepath.Join(worktreesDir, key)
		size := dirSizeBytes(path)
		// We don't know the user's source repo path here — the
		// manifest that recorded it has already been pruned or
		// never existed. Skip the proper `git worktree remove`
		// (which would clean the parent repo's
		// .git/worktrees/<key>/ bookkeeping link) and just nuke
		// the dir directly. Operators can reclaim the bookkeeping
		// later via `git worktree prune` in their own source tree
		// if it matters; the disk-space loss is the real concern
		// and that's reclaimed here.
		if rmErr := os.RemoveAll(path); rmErr != nil {
			fmt.Fprintf(os.Stderr, "warning: worktree GC remove %s: %v\n", path, rmErr)
			release()
			continue
		}
		release()
		// Synthesize a minimal Entry for the result envelope so
		// operators see what was reclaimed. We mark it Orphaned and
		// ArtifactKind="worktree" so JSON consumers can distinguish
		// from real manifest removals.
		result.Removed = append(result.Removed, &Entry{
			Manifest: &Manifest{
				CacheKey:     key,
				ArtifactKind: "worktree",
			},
			SizeBytes: size,
			Orphaned:  true,
		})
		result.FreedBytes += size
	}
}

// dirSizeBytes returns the total disk footprint of a directory tree
// (sum of regular-file sizes). Used to credit the prune result with
// the bytes actually reclaimed by removing a worktree.
func dirSizeBytes(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// selectForPrune is the policy engine. Pulled out for testability —
// the rules are easier to verify against a synthetic entry list than
// against a real on-disk cache.
//
// Semantics:
//
//   - All: short-circuits to "everything". (Ignores other filters by
//     design — operators reaching for --all want a wipe.)
//   - KeepLast N: protects the N newest entries from the GLOBAL set
//     (not from the post-filter candidates). This is the operator
//     safety net — `--older-than 1d --keep-last 3` MUST preserve the
//     3 most recent builds even if all three are older than 1 day.
//   - OrphanOnly: include only entries whose artifact is missing.
//   - OlderThan: include only entries created before (now - dur).
//
// OrphanOnly and OlderThan compose with AND semantics on the
// non-protected entries.
func selectForPrune(all []*Entry, opts PruneOptions, now time.Time) []*Entry {
	if opts.All {
		return append([]*Entry(nil), all...)
	}

	// Protected set: top-N newest from the global list, regardless
	// of any other filter. Computed up-front so the filter loop is
	// a simple membership check.
	protected := map[string]bool{}
	if opts.KeepLast > 0 {
		sorted := append([]*Entry(nil), all...)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
		})
		n := min(opts.KeepLast, len(sorted))
		for _, e := range sorted[:n] {
			protected[e.CacheKey] = true
		}
	}

	candidates := make([]*Entry, 0, len(all))
	for _, e := range all {
		if protected[e.CacheKey] {
			continue
		}
		if opts.OrphanOnly && !e.Orphaned {
			continue
		}
		if opts.OlderThan > 0 && !e.CreatedAt.Before(now.Add(-opts.OlderThan)) {
			continue
		}
		candidates = append(candidates, e)
	}
	return candidates
}

// removeEntry deletes a single cache entry's on-disk + docker state.
// Best-effort: a missing file (orphaned entry) is not an error.
func removeEntry(ctx context.Context, e *Entry) error {
	switch e.ArtifactKind {
	case "jar":
		if e.ArtifactPath != "" {
			if err := os.Remove(e.ArtifactPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove jar %s: %w", e.ArtifactPath, err)
			}
		}
	case "image":
		// docker rmi by tag (not ID) — same tag we set via build.
		// Best-effort: image may already be gone, or docker may be
		// stopped. Either way we still want to clean the manifest.
		if e.ImageTag != "" {
			_ = exec.CommandContext(ctx, "docker", "image", "rm",
				"--force", e.ImageTag).Run()
		}
		// Drop the side-file regardless.
		_ = os.Remove(filepath.Join(CacheDir(), "images", e.CacheKey+".json"))
	}

	// Always drop the manifest. If the artifact removal partially
	// failed we still want the manifest gone so the entry doesn't
	// reappear in `list` as a perpetually-orphaned ghost.
	if err := os.Remove(manifestPath(e.CacheKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove manifest %s: %w", e.CacheKey, err)
	}
	return nil
}
