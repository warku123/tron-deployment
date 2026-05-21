package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// imageMetadata is the bookkeeping record written under
// ${cacheDir}/images/<cache-key>.json. Captures what we built
// locally so Lookup() can verify the image still exists in the
// host's docker storage and so prune (Phase 5) can `docker image
// rm <id>` to actually free the layers.
type imageMetadata struct {
	CacheKey string `json:"cache_key"`
	Tag      string `json:"tag"`
	ImageID  string `json:"image_id"`
}

// buildImage runs gradle to produce a docker image artifact, tags
// the result with the user-supplied build.image_tag, captures the
// image ID, and persists both the build manifest and the per-cache-
// key image bookkeeping JSON. Mirrors buildJAR's lifecycle:
//
//  1. (Optional) clean up stale state from a prior cancelled run.
//  2. Invoke gradle inside the builder container.
//  3. Re-tag the produced image as <build.image_tag>.
//  4. Resolve the image ID via `docker inspect` (so prune knows
//     which sha256 to delete later).
//  5. Validate the image has a runnable ENTRYPOINT (FR-011 for image
//     kind: produced image must be runnable as a java-tron node).
//  6. Persist Manifest + images/<key>.json.
//
// Cancellation: ctx cancellation kills the docker subprocess. We
// don't try to docker-image-rm a half-built image — gradle leaves
// its own partial state inside the container that's already torn
// down with --rm.
func buildImage(ctx context.Context, r *resolved, started time.Time) (*Manifest, error) {
	tag := r.req.ImageTag
	if tag == "" {
		return nil, output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"build.image_tag is required when artifact = image")
	}

	// The wrapper script (dockerBuildScript_Image) snapshots image
	// IDs before + after gradle into <outDir>/<cache-key>-images-{before,after},
	// so we need /out mounted. outDir is the host path that's bound.
	outDir := filepath.Join(CacheDir(), "out")
	// Defer cleanup of the per-cache-key snapshot files. readNewImageIDs
	// also removes them on success, so this is the failure-path
	// safety net — without it, a gradle crash between the two
	// snapshot writes would leave orphan files in the shared out
	// dir (eventually cleaned by Phase 5 prune, but defensible to
	// clean inline too).
	defer func() {
		_ = os.Remove(filepath.Join(outDir, r.cacheKeyStr+"-images-before"))
		_ = os.Remove(filepath.Join(outDir, r.cacheKeyStr+"-images-after"))
	}()

	if err := defaultRunner.RunBuild(ctx, r, outDir, "" /* outTmp unused for image */); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, output.NewErrorf("BUILD_CANCELLED", 130,
				"build cancelled by user").
				WithSuggestions("Re-run when ready")
		}
		return nil, output.NewErrorf("BUILD_FAILED", output.ExitGeneralError,
			"gradle dockerBuild failed: %s", err.Error()).
			WithSuggestions(
				"Inspect the gradle output above for build errors",
				"Verify the source tree's build.gradle defines the dockerBuild task",
			)
	}

	// Read the wrapper's before/after diff. Each line is one
	// image ID that gradle's task produced this run — no race with
	// other host docker activity (gradle's `docker images` runs
	// inside our serialized build window).
	newImageIDs, err := readNewImageIDs(outDir, r.cacheKeyStr)
	if err != nil {
		return nil, output.NewErrorf("BUILD_FAILED", output.ExitGeneralError,
			"locate produced image: %s", err.Error())
	}
	if len(newImageIDs) == 0 {
		return nil, output.NewError("BUILD_FAILED", output.ExitGeneralError,
			"gradle finished but no new docker image appeared on the host").
			WithSuggestions(
				"Verify the gradle task you ran actually produces a docker image",
				"Common task names: dockerBuild, jib, bootBuildImage",
			)
	}
	// Pick the LAST new image — gradle's docker plugin typically
	// produces intermediate layers + a final tagged image; the
	// final one is the deploy artifact. (If gradle produced
	// multiple final images, the user's gradle_task name selected
	// only one, so picking the most-recent is unambiguous.)
	imageID := newImageIDs[len(newImageIDs)-1]
	if err := dockerTag(ctx, imageID, tag); err != nil {
		return nil, output.NewErrorf("BUILD_FAILED", output.ExitGeneralError,
			"docker tag %s %s: %s", imageID, tag, err.Error())
	}
	// Gradle's docker plugin sets its own tag(s) on the image
	// (typically <group>/<name>:<version>, e.g.
	// tronprotocol/java-tron:GreatVoyage-foo). Leaving those in
	// place shadows the upstream namespace in `docker images` and
	// confuses operators debugging deploys — the H block on user-
	// supplied image_tag already addresses this for trond's own
	// tagging; we extend it to gradle-side tags here. Best-effort:
	// removal failure isn't fatal (we just leave the dangling alias).
	if err := stripExtraTags(ctx, imageID, tag); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: could not strip gradle's auto-generated tags from %s: %v\n",
			imageID, err)
	}

	if err := validateImageEntrypoint(ctx, tag); err != nil {
		return nil, output.NewErrorf("INVALID_ARTIFACT", output.ExitGeneralError,
			"produced image is not runnable: %s", err.Error()).
			WithSuggestions(
				"Verify the gradle docker task produces an image with a runnable ENTRYPOINT",
			)
	}

	if err := writeImageMetadata(r.cacheKeyStr, tag, imageID); err != nil {
		return nil, output.NewErrorf("INTERNAL_ERROR", output.ExitGeneralError,
			"persist image metadata: %s", err.Error())
	}

	manifest := &Manifest{
		CacheKey:           r.cacheKeyStr,
		SourcePath:         r.src.Path,
		SourceRevision:     r.src.ResolvedRevision,
		PatchHash:          r.src.PatchHash,
		Dirty:              r.src.DirtyState,
		BuilderImage:       r.imageRef,
		BuilderImageDigest: r.imageDigest,
		JDKVersion:         r.req.JDKVersion,
		ArtifactKind:       "image",
		ImageTag:           tag,
		ImageID:            imageID,
		GradleTask:         r.req.GradleTask,
		GradleArgs:         r.req.GradleArgs,
		Patches:            r.req.Patches,
		Builder:            r.req.Builder,
		Platform:           r.req.Platform,
		DurationMs:         time.Since(started).Milliseconds(),
		CreatedAt:          time.Now().UTC(),
	}
	if err := Save(manifest); err != nil {
		return nil, output.NewErrorf("INTERNAL_ERROR", output.ExitGeneralError,
			"persist manifest: %s", err.Error())
	}
	return manifest, nil
}

// readNewImageIDs computes the (after \ before) diff of TAGGED
// image IDs produced during the gradle run. The wrapper script
// snapshots both sets into per-cache-key files
// `/out/<key>-images-{before,after}`; the diff itself runs here so
// the logic is unit-testable (computeNewImages) without needing a
// real container.
//
// Returns an empty slice when gradle ran but produced no new
// tagged image (the wrapper uses `--filter dangling=false`, so
// multi-stage Dockerfile intermediates are already excluded). The
// per-cache-key snapshot files are removed after a successful
// read so the cache dir doesn't accumulate them across runs.
func readNewImageIDs(outDir, cacheKey string) ([]string, error) {
	beforePath := filepath.Join(outDir, cacheKey+"-images-before")
	afterPath := filepath.Join(outDir, cacheKey+"-images-after")

	before, err := readSnapshot(beforePath)
	if err != nil {
		return nil, fmt.Errorf("read before snapshot: %w", err)
	}
	after, err := readSnapshot(afterPath)
	if err != nil {
		return nil, fmt.Errorf("read after snapshot: %w", err)
	}

	// Best-effort cleanup. Leaving them around is harmless except
	// for disk noise; future runs overwrite via the wrapper.
	_ = os.Remove(beforePath)
	_ = os.Remove(afterPath)

	return computeNewImages(before, after), nil
}

// readSnapshot parses one line-per-image-id file produced by the
// wrapper script. Trims whitespace; skips blanks.
func readSnapshot(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ids []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ids = append(ids, line)
	}
	return ids, nil
}

// computeNewImages returns the entries in `after` that are not in
// `before`. Order preserves `after`'s original order so downstream
// callers can rely on positional invariants. Unit-tested for the
// multi-stage Dockerfile case where intermediate layers would
// otherwise confuse the picker.
func computeNewImages(before, after []string) []string {
	seen := make(map[string]bool, len(before))
	for _, id := range before {
		seen[id] = true
	}
	var diff []string
	for _, id := range after {
		if !seen[id] {
			diff = append(diff, id)
		}
	}
	return diff
}

func dockerTag(ctx context.Context, imageID, tag string) error {
	cmd := exec.CommandContext(ctx, "docker", "tag", imageID, tag)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// stripExtraTags removes every tag on imageID except keepTag. Used
// to clean up gradle's auto-generated tag(s) (e.g.
// tronprotocol/java-tron:GreatVoyage-foo) after trond re-tags with
// the user's build.image_tag. We `docker image rm <tag>` rather
// than `docker rmi <id>` — the underlying image layers stay alive
// on the kept tag, only the auto-generated aliases get removed.
//
// Returns the first removal error encountered; the manifest /
// trond cache is unaffected either way.
//
// `<none>` aliases (dangling tags from a botched gradle plugin) are
// skipped via a substring check rather than equality with the
// historical `<none>:<none>` literal — docker version drift
// occasionally changes the print format and we'd rather skip a
// few stray dangling entries than wrongly try to `docker image rm
// "<none>"`.
func stripExtraTags(ctx context.Context, imageID, keepTag string) error {
	tags, err := imageTags(ctx, imageID)
	if err != nil {
		return err
	}
	for _, t := range tags {
		if t == keepTag || strings.Contains(t, "<none>") {
			continue
		}
		// `docker image rm <tag>` decrements the tag's ref count;
		// when that tag has no other references the alias is
		// dropped, and only when the LAST tag is removed does the
		// underlying image actually get GC'd. Since keepTag still
		// references the image, this only strips aliases.
		cmd := exec.CommandContext(ctx, "docker", "image", "rm", t)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("untag %s: %w: %s", t, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// imageTags returns all repo:tag strings currently aliasing the
// given image ID. Used by stripExtraTags.
func imageTags(ctx context.Context, imageID string) ([]string, error) {
	// docker inspect --format with range emits one tag per line.
	cmd := exec.CommandContext(ctx, "docker", "inspect",
		"--format={{range .RepoTags}}{{.}}\n{{end}}", imageID)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", imageID, err)
	}
	var tags []string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tags = append(tags, line)
	}
	return tags, nil
}

// validateImageEntrypoint asserts the image has a non-empty
// ENTRYPOINT or CMD — FR-011's image-side analogue. Run via
// `docker inspect`; we don't try to actually `docker run` the image
// because that's expensive and we don't have arguments to test it
// with.
//
// Uses {{len ...}} to compare numbers (0/0 = both empty) rather
// than string-compare the formatted array — docker version drift
// changes the printed representation but never the array length.
func validateImageEntrypoint(ctx context.Context, tag string) error {
	cmd := exec.CommandContext(ctx, "docker", "inspect",
		"--format={{len .Config.Entrypoint}}/{{len .Config.Cmd}}", tag)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("docker inspect %s: %w", tag, err)
	}
	got := strings.TrimSpace(string(out))
	if got == "0/0" {
		return fmt.Errorf("image %s has neither ENTRYPOINT nor CMD", tag)
	}
	return nil
}

func writeImageMetadata(cacheKey, tag, imageID string) error {
	if err := os.MkdirAll(filepath.Join(CacheDir(), "images"), 0o700); err != nil {
		return err
	}
	meta := imageMetadata{CacheKey: cacheKey, Tag: tag, ImageID: imageID}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(CacheDir(), "images", cacheKey+".json")
	return os.WriteFile(path, data, 0o600)
}

// readImageMetadata returns the bookkeeping record for a cache key,
// or os.ErrNotExist when no image was produced for that key. Used
// by cache.Lookup() to verify the local image still exists before
// declaring a cache hit.
func readImageMetadata(cacheKey string) (*imageMetadata, error) {
	path := filepath.Join(CacheDir(), "images", cacheKey+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var meta imageMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("decode image metadata: %w", err)
	}
	return &meta, nil
}

// imageTagExistsLocally checks whether the docker tag is still
// resolvable in the host's image store. We deliberately check tag
// (not image ID): a `docker rmi <tag>` may detach the tag while the
// underlying image stays alive via another tag, but compose's
// `image: <tag>` field is what trond renders — so tag presence is
// the authoritative signal. Tag-side check also catches the case
// where the user retagged trond's image to something else.
func imageTagExistsLocally(ctx context.Context, tag string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect",
		"--format={{.Id}}", tag)
	return cmd.Run() == nil
}
