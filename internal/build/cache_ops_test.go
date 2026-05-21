package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedJARManifest creates a manifest + a real JAR file under the test
// cache dir, returning the cache key and the manifest. Used by every
// test in this file because the cache_ops layer's contract is "walk
// what's actually on disk", so a stub-manifest-only fixture would
// give a false positive.
func seedJARManifest(t *testing.T, key string, createdAt time.Time, size int) *Manifest {
	t.Helper()
	if err := EnsureCacheDirs(); err != nil {
		t.Fatalf("EnsureCacheDirs: %v", err)
	}
	artifactPath := filepath.Join(CacheDir(), "out", key+".jar")
	if err := os.WriteFile(artifactPath, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write jar: %v", err)
	}
	m := &Manifest{
		CacheKey:           key,
		SourcePath:         "/some/src",
		SourceRevision:     "abc1234567890abcdef1234567890abcdef12345",
		BuilderImage:       "eclipse-temurin:8-jdk-jammy",
		BuilderImageDigest: "sha256:aaaa",
		JDKVersion:         "8",
		ArtifactKind:       "jar",
		ArtifactPath:       artifactPath,
		GradleTask:         "shadowJar",
		Builder:            "docker",
		Platform:           "linux/amd64",
		CreatedAt:          createdAt,
	}
	if err := Save(m); err != nil {
		t.Fatalf("Save manifest: %v", err)
	}
	return m
}

// TestListEntries_EmptyCache returns nil without error.
func TestListEntries_EmptyCache(t *testing.T) {
	withTempBaseDir(t)
	entries, err := ListEntries(context.Background())
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("empty cache should yield 0 entries; got %d", len(entries))
	}
}

// TestListEntries_SortedNewestFirst pins the default order — the
// `trond build list` table relies on this.
func TestListEntries_SortedNewestFirst(t *testing.T) {
	withTempBaseDir(t)
	base := time.Now()
	seedJARManifest(t, "key-old", base.Add(-2*time.Hour), 100)
	seedJARManifest(t, "key-mid", base.Add(-1*time.Hour), 200)
	seedJARManifest(t, "key-new", base, 300)

	entries, err := ListEntries(context.Background())
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	want := []string{"key-new", "key-mid", "key-old"}
	for i, e := range entries {
		if e.CacheKey != want[i] {
			t.Errorf("entries[%d].CacheKey = %q; want %q (must be newest-first)",
				i, e.CacheKey, want[i])
		}
	}
}

// TestListEntries_SkipsOrphansByDefault: a manifest whose JAR has
// been deleted is hidden by default and shown with IncludeOrphans.
func TestListEntries_SkipsOrphansByDefault(t *testing.T) {
	withTempBaseDir(t)
	now := time.Now()
	seedJARManifest(t, "key-live", now, 100)
	orphan := seedJARManifest(t, "key-orphan", now.Add(-time.Minute), 100)
	if err := os.Remove(orphan.ArtifactPath); err != nil {
		t.Fatalf("delete jar: %v", err)
	}

	t.Run("default hides orphan", func(t *testing.T) {
		entries, err := ListEntries(context.Background())
		if err != nil {
			t.Fatalf("ListEntries: %v", err)
		}
		if len(entries) != 1 || entries[0].CacheKey != "key-live" {
			t.Errorf("default list should hide orphans; got %v", keys(entries))
		}
	})
	t.Run("IncludeOrphans shows both", func(t *testing.T) {
		entries, err := ListEntries(context.Background(), IncludeOrphans())
		if err != nil {
			t.Fatalf("ListEntries: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("IncludeOrphans should yield 2 entries; got %d", len(entries))
		}
		// Find the orphan; assert Orphaned + size 0.
		var found bool
		for _, e := range entries {
			if e.CacheKey == "key-orphan" {
				found = true
				if !e.Orphaned {
					t.Error("key-orphan should be Orphaned")
				}
				if e.SizeBytes != 0 {
					t.Errorf("orphan SizeBytes = %d; want 0", e.SizeBytes)
				}
			}
		}
		if !found {
			t.Error("orphan entry missing from IncludeOrphans list")
		}
	})
}

// TestInspectEntry_FullKey: exact match returns the entry.
func TestInspectEntry_FullKey(t *testing.T) {
	withTempBaseDir(t)
	seedJARManifest(t, "abc12345-bdeadbeef", time.Now(), 100)
	e, err := InspectEntry(context.Background(), "abc12345-bdeadbeef")
	if err != nil {
		t.Fatalf("InspectEntry: %v", err)
	}
	if e.CacheKey != "abc12345-bdeadbeef" {
		t.Errorf("CacheKey = %q; want exact match", e.CacheKey)
	}
	if e.SizeBytes != 100 {
		t.Errorf("SizeBytes = %d; want 100", e.SizeBytes)
	}
}

// TestInspectEntry_PrefixUnambiguous: a unique prefix resolves to
// the single matching entry. Operators don't have to type the full
// 22-char key from a `list` table.
func TestInspectEntry_PrefixUnambiguous(t *testing.T) {
	withTempBaseDir(t)
	seedJARManifest(t, "abc12345-bdeadbeef", time.Now(), 100)
	seedJARManifest(t, "def98765-bcafebabe", time.Now(), 200)
	e, err := InspectEntry(context.Background(), "abc")
	if err != nil {
		t.Fatalf("InspectEntry: %v", err)
	}
	if e.CacheKey != "abc12345-bdeadbeef" {
		t.Errorf("prefix 'abc' should match abc12345-... ; got %q", e.CacheKey)
	}
}

// TestInspectEntry_PrefixAmbiguous: returns ErrAmbiguousPrefix so the
// CLI can map to a friendly error code.
func TestInspectEntry_PrefixAmbiguous(t *testing.T) {
	withTempBaseDir(t)
	seedJARManifest(t, "abc12345-bdeadbeef", time.Now(), 100)
	seedJARManifest(t, "abc98765-bcafebabe", time.Now(), 200)
	_, err := InspectEntry(context.Background(), "abc")
	if !errors.Is(err, ErrAmbiguousPrefix) {
		t.Errorf("expected ErrAmbiguousPrefix; got %v", err)
	}
	// The error message should list the candidates so the operator
	// knows what to disambiguate to.
	if msg := err.Error(); !contains(msg, "abc12345") || !contains(msg, "abc98765") {
		t.Errorf("ambiguity error should list candidates; got %q", msg)
	}
}

// TestInspectEntry_NoMatch: ErrNoMatch sentinel.
func TestInspectEntry_NoMatch(t *testing.T) {
	withTempBaseDir(t)
	seedJARManifest(t, "abc12345-bdeadbeef", time.Now(), 100)
	_, err := InspectEntry(context.Background(), "xyz")
	if !errors.Is(err, ErrNoMatch) {
		t.Errorf("expected ErrNoMatch; got %v", err)
	}
}

// TestSelectForPrune_All overrides all other filters.
func TestSelectForPrune_All(t *testing.T) {
	now := time.Now()
	entries := []*Entry{
		{Manifest: &Manifest{CacheKey: "fresh", CreatedAt: now}},
		{Manifest: &Manifest{CacheKey: "old", CreatedAt: now.Add(-30 * 24 * time.Hour)}},
	}
	got := selectForPrune(entries, PruneOptions{All: true}, now)
	if len(got) != 2 {
		t.Errorf("--all should select everything; got %d/%d", len(got), len(entries))
	}
}

// TestSelectForPrune_OrphanOnly restricts to orphans regardless of age.
func TestSelectForPrune_OrphanOnly(t *testing.T) {
	now := time.Now()
	entries := []*Entry{
		{Manifest: &Manifest{CacheKey: "live-fresh", CreatedAt: now}, Orphaned: false},
		{Manifest: &Manifest{CacheKey: "orphan-fresh", CreatedAt: now}, Orphaned: true},
		{Manifest: &Manifest{CacheKey: "live-old", CreatedAt: now.Add(-30 * 24 * time.Hour)}, Orphaned: false},
	}
	got := selectForPrune(entries, PruneOptions{OrphanOnly: true}, now)
	if len(got) != 1 || got[0].CacheKey != "orphan-fresh" {
		t.Errorf("orphan-only should match exactly the orphan; got %v", keys(got))
	}
}

// TestSelectForPrune_OlderThan filters by age.
func TestSelectForPrune_OlderThan(t *testing.T) {
	now := time.Now()
	entries := []*Entry{
		{Manifest: &Manifest{CacheKey: "1h-old", CreatedAt: now.Add(-1 * time.Hour)}},
		{Manifest: &Manifest{CacheKey: "10h-old", CreatedAt: now.Add(-10 * time.Hour)}},
		{Manifest: &Manifest{CacheKey: "30d-old", CreatedAt: now.Add(-30 * 24 * time.Hour)}},
	}
	got := selectForPrune(entries, PruneOptions{OlderThan: 24 * time.Hour}, now)
	if len(got) != 1 || got[0].CacheKey != "30d-old" {
		t.Errorf("--older-than 24h should match only 30d-old; got %v", keys(got))
	}
}

// TestSelectForPrune_KeepLast preserves the N newest even if other
// filters would have selected them. This is the safety-net combo
// operators rely on: `prune --older-than 1d --keep-last 3` never
// wipes their three most-recent builds.
func TestSelectForPrune_KeepLast(t *testing.T) {
	now := time.Now()
	entries := []*Entry{
		{Manifest: &Manifest{CacheKey: "newest", CreatedAt: now}},
		{Manifest: &Manifest{CacheKey: "mid-1", CreatedAt: now.Add(-2 * time.Hour)}},
		{Manifest: &Manifest{CacheKey: "mid-2", CreatedAt: now.Add(-3 * time.Hour)}},
		{Manifest: &Manifest{CacheKey: "oldest", CreatedAt: now.Add(-100 * time.Hour)}},
	}
	got := selectForPrune(entries, PruneOptions{
		OlderThan: 1 * time.Hour, // would otherwise hit all but `newest`
		KeepLast:  3,             // protect the 3 newest
	}, now)
	if len(got) != 1 || got[0].CacheKey != "oldest" {
		t.Errorf("KeepLast 3 should leave only `oldest` pruneable; got %v", keys(got))
	}
}

// TestPrune_DryRunDoesNotTouchDisk: plan is computed, but the on-disk
// state is unchanged.
func TestPrune_DryRunDoesNotTouchDisk(t *testing.T) {
	withTempBaseDir(t)
	m := seedJARManifest(t, "key-1", time.Now().Add(-10*24*time.Hour), 100)

	res, err := Prune(context.Background(), PruneOptions{All: true, DryRun: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !res.DryRun {
		t.Error("DryRun should be reflected in result")
	}
	if len(res.Plan) != 1 {
		t.Fatalf("plan should have 1 entry; got %d", len(res.Plan))
	}
	if len(res.Removed) != 0 {
		t.Errorf("DryRun should not remove anything; Removed had %d", len(res.Removed))
	}
	if res.FreedBytes != 100 {
		t.Errorf("FreedBytes = %d; want 100 (dry-run still reports what WOULD be freed)", res.FreedBytes)
	}
	// Artifact + manifest must still be there.
	if _, err := os.Stat(m.ArtifactPath); err != nil {
		t.Errorf("DryRun deleted the JAR: %v", err)
	}
	if _, err := os.Stat(manifestPath(m.CacheKey)); err != nil {
		t.Errorf("DryRun deleted the manifest: %v", err)
	}
}

// TestPrune_AllActuallyDeletes: without DryRun, files are gone.
func TestPrune_AllActuallyDeletes(t *testing.T) {
	withTempBaseDir(t)
	m := seedJARManifest(t, "key-doomed", time.Now(), 100)

	res, err := Prune(context.Background(), PruneOptions{All: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Removed) != 1 {
		t.Fatalf("Removed should have 1 entry; got %d", len(res.Removed))
	}
	if _, err := os.Stat(m.ArtifactPath); !os.IsNotExist(err) {
		t.Errorf("JAR should be deleted; stat err = %v", err)
	}
	if _, err := os.Stat(manifestPath(m.CacheKey)); !os.IsNotExist(err) {
		t.Errorf("manifest should be deleted; stat err = %v", err)
	}
}

// TestPrune_OrphanCleansManifestEvenWithoutArtifact: an orphan entry
// (manifest only, no JAR) is still removed by --orphan, leaving the
// cache truly empty.
func TestPrune_OrphanCleansManifestEvenWithoutArtifact(t *testing.T) {
	withTempBaseDir(t)
	m := seedJARManifest(t, "orphan", time.Now(), 100)
	if err := os.Remove(m.ArtifactPath); err != nil {
		t.Fatalf("delete jar: %v", err)
	}

	res, err := Prune(context.Background(), PruneOptions{OrphanOnly: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Removed) != 1 {
		t.Fatalf("Removed should have 1 entry; got %d", len(res.Removed))
	}
	if _, err := os.Stat(manifestPath(m.CacheKey)); !os.IsNotExist(err) {
		t.Errorf("orphan manifest should be removed; stat err = %v", err)
	}
}

// TestPrune_HonorsContextCancellation is the review-pass-5 guard:
// a cancelled ctx must stop the per-entry loop promptly, even mid-
// plan. Without the check, Ctrl+C during a long prune (many image
// entries with slow `docker image rm` calls) only takes effect at
// the next docker round-trip. Plan stays populated so the caller
// can still see what WOULD have been done.
func TestPrune_HonorsContextCancellation(t *testing.T) {
	withTempBaseDir(t)
	for i := range 5 {
		seedJARManifest(t, fmt.Sprintf("entry-%d", i), time.Now(), 100)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE Prune to guarantee the first iteration trips the check

	res, err := Prune(ctx, PruneOptions{All: true})
	if err == nil {
		t.Fatal("expected ctx.Err() to surface from Prune; got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled; got %v", err)
	}
	// Plan should still be populated (we want the caller to know
	// what would have been removed at cancellation time).
	if len(res.Plan) != 5 {
		t.Errorf("Plan should still have 5 entries on cancel; got %d", len(res.Plan))
	}
	// Removed should be empty — we cancelled before any deletion.
	if len(res.Removed) != 0 {
		t.Errorf("Removed should be empty when cancelled before first iteration; got %d", len(res.Removed))
	}
}

// TestPrune_AcquiresCacheLock pins the review-pass-4 race fix: Prune
// MUST hold the same per-key flock that builder.Run() acquires, so a
// concurrent build of the entry being pruned cannot interleave with
// our manifest+artifact deletion. Verified by holding the flock
// externally and observing Prune's "lock unavailable" skip path.
//
// (We can't easily reproduce the data corruption directly in a unit
// test — too much process orchestration. This test pins the
// contract: "if the lock can't be acquired, the entry is skipped",
// which is the post-condition that protects the cache.)
func TestPrune_AcquiresCacheLock(t *testing.T) {
	withTempBaseDir(t)
	if err := EnsureCacheDirs(); err != nil {
		t.Fatalf("EnsureCacheDirs: %v", err)
	}
	locked := seedJARManifest(t, "locked-by-build", time.Now(), 100)

	// Simulate a concurrent build: hold the flock for this entry's
	// key. Prune must observe the lock and skip without touching
	// anything.
	release, err := AcquireCacheLock(CacheDir(), locked.CacheKey)
	if err != nil {
		t.Fatalf("test AcquireCacheLock: %v", err)
	}
	t.Cleanup(release)

	res, err := Prune(context.Background(), PruneOptions{All: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// Plan still lists the entry (we never released the lock until
	// AFTER Prune returns), but Removed must NOT contain it because
	// Prune couldn't acquire the lock.
	if len(res.Removed) != 0 {
		t.Errorf("Removed should be empty when lock is held externally; got %v", keys(res.Removed))
	}
	// And the JAR + manifest are still on disk — no partial
	// deletion.
	if _, statErr := os.Stat(locked.ArtifactPath); statErr != nil {
		t.Errorf("locked JAR should be untouched; stat err = %v", statErr)
	}
	if _, statErr := os.Stat(manifestPath(locked.CacheKey)); statErr != nil {
		t.Errorf("locked manifest should be untouched; stat err = %v", statErr)
	}
}

// TestPrune_FreedBytesOnlyCountsActuallyRemoved is the review-pass-4
// regression guard: PruneResult.FreedBytes MUST reflect bytes
// actually reclaimed, not the plan's optimistic total. Otherwise an
// MCP agent surfacing "freed N bytes" would report a number that
// doesn't match the bytes the OS actually got back.
//
// Constructed scenario: two entries in the plan, simulate partial
// failure by pre-deleting one entry's manifest under our feet so
// removeEntry's os.Remove(manifestPath) fails. The successful entry
// contributes its bytes; the failed entry does not.
func TestPrune_FreedBytesOnlyCountsActuallyRemoved(t *testing.T) {
	withTempBaseDir(t)
	good := seedJARManifest(t, "good", time.Now(), 500)
	bad := seedJARManifest(t, "bad", time.Now().Add(-time.Hour), 1000)

	// Sabotage the "bad" entry's artifact: swap the JAR file for a
	// non-empty directory at the same path. removeEntry's
	// os.Remove(e.ArtifactPath) refuses to delete a non-empty dir
	// → entry removal fails. ListEntries still parses the (intact)
	// manifest, so the entry IS in the plan but won't end up in
	// Removed. Faithfully models real failures the reviewer flagged
	// (docker rmi wedged, fs permissions, etc.).
	if err := os.Remove(bad.ArtifactPath); err != nil {
		t.Fatalf("setup: remove jar for sabotage: %v", err)
	}
	if err := os.MkdirAll(bad.ArtifactPath, 0o755); err != nil {
		t.Fatalf("setup: mkdir jar path for sabotage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad.ArtifactPath, "wedge"), []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: wedge file: %v", err)
	}

	res, err := Prune(context.Background(), PruneOptions{All: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// Plan has both entries; Removed has only the good one.
	if len(res.Plan) != 2 {
		t.Errorf("Plan should list 2 entries; got %d", len(res.Plan))
	}
	if len(res.Removed) != 1 || res.Removed[0].CacheKey != "good" {
		t.Fatalf("Removed should contain only 'good'; got %v", keys(res.Removed))
	}
	// THE FIX: FreedBytes must equal Removed-only sum, NOT Plan sum.
	if res.FreedBytes != 500 {
		t.Errorf("FreedBytes = %d; want 500 (bytes actually reclaimed, not plan total of 1500)", res.FreedBytes)
	}
	// And good's artifact should be gone.
	if _, statErr := os.Stat(good.ArtifactPath); !os.IsNotExist(statErr) {
		t.Errorf("good JAR should be deleted; stat err = %v", statErr)
	}
}

// TestPrune_FreedBytesOnDryRunMatchesPlan: on dry-run, FreedBytes
// reflects what WOULD be freed (== plan sum), since Removed is
// empty by design. Without this branch in Prune, the post-Removed-
// loop accumulation would report 0 freed for dry-runs.
func TestPrune_FreedBytesOnDryRunMatchesPlan(t *testing.T) {
	withTempBaseDir(t)
	seedJARManifest(t, "a", time.Now(), 100)
	seedJARManifest(t, "b", time.Now(), 200)

	res, err := Prune(context.Background(), PruneOptions{All: true, DryRun: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.FreedBytes != 300 {
		t.Errorf("DryRun FreedBytes = %d; want 300 (full plan total)", res.FreedBytes)
	}
}

// TestPrune_GCsOrphanWorktrees is the review-pass-8 H2 guard: a
// worktree dir whose cache key has no live manifest AND no active
// flock is an orphan (SIGKILL between setupWorktree and the
// deferred cleanup; process panic; aborted ctrl-C between defer
// registration and run). Prune MUST reclaim those — they're
// 100-200 MB each for java-tron and never disappear on their own.
func TestPrune_GCsOrphanWorktrees(t *testing.T) {
	withTempBaseDir(t)
	if err := EnsureCacheDirs(); err != nil {
		t.Fatalf("EnsureCacheDirs: %v", err)
	}

	// Plant an orphan worktree dir with some content to measure
	// FreedBytes against.
	orphanKey := "orphan-key-no-manifest"
	orphanPath := filepath.Join(CacheDir(), "worktrees", orphanKey)
	if err := os.MkdirAll(orphanPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanPath, "stale-data.bin"),
		make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	// Also plant a non-orphan worktree (with a corresponding manifest)
	// to confirm GC does NOT touch it.
	live := seedJARManifest(t, "live-key", time.Now(), 100)
	liveWorktreePath := filepath.Join(CacheDir(), "worktrees", live.CacheKey)
	if err := os.MkdirAll(liveWorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveWorktreePath, "in-use.bin"),
		make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}

	// Prune with a no-op policy (nothing to remove from manifests) —
	// the GC pass still runs because it's unconditional.
	res, err := Prune(context.Background(), PruneOptions{OrphanOnly: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// Orphan must be gone.
	if _, statErr := os.Stat(orphanPath); !os.IsNotExist(statErr) {
		t.Errorf("orphan worktree at %s should be reclaimed; stat err = %v", orphanPath, statErr)
	}
	// Live worktree must be untouched (the live manifest's cache key
	// is in the validKeys set, so its worktree skips the GC).
	if _, statErr := os.Stat(liveWorktreePath); statErr != nil {
		t.Errorf("live worktree at %s should NOT have been GC'd; stat err = %v", liveWorktreePath, statErr)
	}

	// Removed list should contain the orphan with ArtifactKind="worktree"
	// so JSON consumers can distinguish it from a manifest removal.
	var foundOrphan bool
	for _, e := range res.Removed {
		if e.CacheKey == orphanKey {
			foundOrphan = true
			if e.ArtifactKind != "worktree" {
				t.Errorf("orphan worktree's ArtifactKind = %q; want 'worktree' so the prune JSON disambiguates", e.ArtifactKind)
			}
			if !e.Orphaned {
				t.Error("orphan worktree should be marked Orphaned=true in result")
			}
			if e.SizeBytes < 4096 {
				t.Errorf("FreedBytes for orphan = %d; want >= 4096 (the planted file)", e.SizeBytes)
			}
		}
	}
	if !foundOrphan {
		t.Errorf("orphan worktree not surfaced in result.Removed; got %d entries", len(res.Removed))
	}
}

// TestPrune_SkipsLockedOrphanWorktree pins the safety case: an
// orphan worktree dir whose cache key flock is currently held
// (representing a concurrent in-flight build) is NOT GC'd. Without
// this, a slow worktree setup followed by a prune-in-parallel could
// rip out the worktree dir mid-gradle-run.
func TestPrune_SkipsLockedOrphanWorktree(t *testing.T) {
	withTempBaseDir(t)
	if err := EnsureCacheDirs(); err != nil {
		t.Fatalf("EnsureCacheDirs: %v", err)
	}

	lockedKey := "locked-by-concurrent-build"
	wtPath := filepath.Join(CacheDir(), "worktrees", lockedKey)
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Hold the flock for this key, simulating an active setupWorktree.
	release, err := AcquireCacheLock(CacheDir(), lockedKey)
	if err != nil {
		t.Fatalf("test AcquireCacheLock: %v", err)
	}
	t.Cleanup(release)

	// No live manifest for this key → it LOOKS like an orphan to the
	// GC scan, but the flock check protects it.
	_, err = Prune(context.Background(), PruneOptions{OrphanOnly: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Errorf("locked worktree at %s should NOT be GC'd; stat err = %v", wtPath, statErr)
	}
}

// --- helpers ---

func keys(entries []*Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.CacheKey
	}
	return out
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
