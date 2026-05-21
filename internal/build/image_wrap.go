package build

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

//go:embed jar_wrap.dockerfile
var jarWrapDockerfileTemplate string

// buildImageJarWrap is the orchestrating entry for the jar-wrap
// strategy: produce the JAR via a recursive Run with
// ArtifactKind=jar (full cache reuse, all the Phase 1-2 safety
// invariants), then hand the JAR off to buildImageFromJAR for the
// docker-build step.
//
// The two cache entries (JAR + image) coexist because their cache
// keys differ on ArtifactKind. If the user later runs trond apply
// with artifact=jar against the same source, the cached JAR is
// reused with no work; the wrapped image stays separate.
func buildImageJarWrap(ctx context.Context, r *resolved, started time.Time) (*Manifest, error) {
	// Step 1: get the JAR. Recurse through Run with a JAR-shaped
	// Request so the inner call hits the JAR cache (extraFold drops
	// the image-only fields). See jarReqForWrap for the field-level
	// invariants this depends on.
	//
	// Lock ordering invariant: the outer Run is currently holding the
	// IMAGE cache-key flock. The recursive call below acquires the
	// JAR cache-key flock. Two trond processes building the same
	// source with image_strategy=jar-wrap therefore both acquire
	// IMAGE-lock first, then JAR-lock — no flip, no deadlock. Any
	// future refactor MUST preserve this order.
	jarResult, err := Run(ctx, jarReqForWrap(r.req))
	if err != nil {
		return nil, err
	}
	if jarResult.ArtifactPath == "" {
		return nil, output.NewErrorf("BUILD_FAILED", output.ExitGeneralError,
			"jar-wrap: inner JAR build returned no artifact_path")
	}

	// Step 2: wrap the JAR into an image.
	return buildImageFromJAR(ctx, r, jarResult.ArtifactPath, jarResult.SHA256, started)
}

// buildImageFromJAR is Phase 5d's "jar-wrap" image strategy: produce
// the JAR via Phase 1-2's flow (caller has already done this), then
// `docker build` against trond's embedded Dockerfile that COPYs the
// JAR into a pinned eclipse-temurin runtime.
//
// Unlike Phase 3's gradle-strategy buildImage:
//
//   - No docker.sock bind-mount into a builder container. We invoke
//     `docker build` directly from the host. The source tree is NOT
//     mounted into anything — only the JAR + Dockerfile sit in a
//     small per-cache-key context dir.
//   - Cross-arch IS supported here (Phase 3 rejected it). docker
//     build's `--platform` works correctly with our COPY-only
//     Dockerfile; there's no "host daemon arch wins" complication
//     because we don't mount docker.sock from inside a builder.
//
// The returned manifest's CacheKey is the IMAGE cache key (different
// from the JAR's, by virtue of ArtifactKind=image + ImageStrategy
// participating in extraFold). Both cache entries coexist.
func buildImageFromJAR(ctx context.Context, r *resolved, jarPath, jarSHA256 string, started time.Time) (*Manifest, error) {
	tag := r.req.ImageTag
	if tag == "" {
		return nil, output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"build.image_tag is required when artifact = image")
	}

	// Per-cache-key context dir (NOT the global out/ — keeps the
	// build context small + makes `docker build`'s "sending build
	// context to daemon" line show only the JAR + Dockerfile).
	contextDir := filepath.Join(CacheDir(), "wrap", r.cacheKeyStr)
	if err := os.RemoveAll(contextDir); err != nil {
		return nil, output.NewErrorf("INTERNAL_ERROR", output.ExitGeneralError,
			"clean prior wrap context: %s", err.Error())
	}
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return nil, output.NewErrorf("INTERNAL_ERROR", output.ExitGeneralError,
			"create wrap context: %s", err.Error())
	}
	defer func() {
		// Cleanup the context dir after build (or attempted build).
		// The JAR lives in the JAR cache; we just copied it in. The
		// Dockerfile is regenerated from the embedded template.
		// Nothing here needs to outlive the build invocation.
		_ = os.RemoveAll(contextDir)
	}()

	// Copy the JAR into the build context. Hardlink would be faster
	// but cross-filesystem hardlink fails; the simple copy is robust
	// and the JAR write happens once per cache miss anyway.
	jarName := filepath.Base(jarPath)
	if err := copyFileForWrap(jarPath, filepath.Join(contextDir, jarName)); err != nil {
		return nil, output.NewErrorf("INTERNAL_ERROR", output.ExitGeneralError,
			"stage jar into build context: %s", err.Error())
	}

	// Render the embedded Dockerfile with the per-build placeholders.
	// Arch triplet follows Debian's multi-arch convention so the
	// tcmalloc lib resolves to the correct path inside the image.
	dockerfile := strings.NewReplacer(
		"{{BASE_IMAGE}}", r.imageRef,
		"{{JAR_NAME}}", jarName,
		"{{ARCH_TRIPLET}}", archTripletForPlatform(r.req.Platform),
		"{{SOURCE_REVISION}}", r.src.ResolvedRevision,
		"{{CACHE_KEY}}", r.cacheKeyStr,
		"{{BUILD_TIME}}", time.Now().UTC().Format(time.RFC3339),
	).Replace(jarWrapDockerfileTemplate)
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return nil, output.NewErrorf("INTERNAL_ERROR", output.ExitGeneralError,
			"write Dockerfile: %s", err.Error())
	}

	// `docker build` invocation. argv-only (FR-022).
	buildArgs := []string{
		"build",
		"-t", tag,
		"-f", filepath.Join(contextDir, "Dockerfile"),
	}
	if r.req.Platform != "" {
		// jar-wrap supports cross-arch because the Dockerfile is
		// COPY-only — there's no `RUN` step that'd need QEMU to
		// emulate. The produced image still matches --platform.
		buildArgs = append(buildArgs, "--platform", r.req.Platform)
	}
	buildArgs = append(buildArgs, contextDir)

	cmd := exec.CommandContext(ctx, "docker", buildArgs...)
	cmd.Stdout = os.Stderr // build output to stderr; stdout reserved for JSON
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, output.NewErrorf("BUILD_CANCELLED", 130,
				"build cancelled by user").
				WithSuggestions("Re-run when ready")
		}
		return nil, output.NewErrorf("BUILD_FAILED", output.ExitGeneralError,
			"docker build of jar-wrap image failed: %s", err.Error()).
			WithSuggestions(
				"Inspect the docker build output above for errors",
				"Verify the JAR at "+jarPath+" is intact",
			)
	}

	// Resolve the image ID via `docker inspect`. Unlike Phase 3's
	// gradle path we know exactly which tag we just produced, so no
	// before/after snapshot trick needed — just inspect by tag.
	imageID, err := dockerInspectImageID(ctx, tag)
	if err != nil {
		return nil, output.NewErrorf("BUILD_FAILED", output.ExitGeneralError,
			"locate produced image %s: %s", tag, err.Error())
	}
	if err := validateImageEntrypoint(ctx, tag); err != nil {
		return nil, output.NewErrorf("INVALID_ARTIFACT", output.ExitGeneralError,
			"produced image is not runnable: %s", err.Error())
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
		SHA256:             jarSHA256, // sha256 of the wrapped JAR, for traceability
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

// jarReqForWrap derives the inner JAR Request from an outer
// image-strategy=jar-wrap Request. Centralised here so both
// buildImageJarWrap and unit tests share the exact mutation rules:
//
//   - ArtifactKind flips to "jar" so the inner Run cache-keys as a JAR
//     (extraFold drops ImageStrategy/ImageTag for jar artifacts).
//   - ImageTag / ImageStrategy are cleared so the inner Run never
//     recurses back into jar-wrap, and so the JAR cache key matches
//     what an explicit `artifact=jar` build produces (true cache reuse
//     across both invocation styles).
//   - GradleTask: if the outer request inherited the artifact=image
//     default of "dockerBuild", swap to the JAR default "shadowJar".
//     Any explicit user override is preserved.
func jarReqForWrap(outer Request) Request {
	jarReq := outer
	jarReq.ArtifactKind = "jar"
	jarReq.ImageTag = ""
	jarReq.ImageStrategy = ""
	if jarReq.GradleTask == "dockerBuild" {
		jarReq.GradleTask = "shadowJar"
	}
	return jarReq
}

// dockerInspectImageID looks up the image ID by tag. Unlike the Phase
// 3 gradle path we don't need to guess which image was created — we
// own the tag.
func dockerInspectImageID(ctx context.Context, tag string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect",
		"--format={{.Id}}", tag)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker image inspect %s: %w", tag, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// archTripletForPlatform maps trond's docker platform string to the
// Debian multi-arch triplet used in /usr/lib/<triplet>/<libname>.
// Eclipse Temurin's `*-jdk-jammy` base is Ubuntu 22.04 (Debian-derived)
// so the same triplet convention applies to our LD_PRELOAD path.
//
// Defaults to amd64 for the (rare) case of an empty Platform — same
// rule as the rest of the build pipeline's Phase 5d default chain.
func archTripletForPlatform(platform string) string {
	switch platform {
	case "linux/arm64":
		return "aarch64-linux-gnu"
	default:
		return "x86_64-linux-gnu"
	}
}

// copyFileForWrap is a small helper because using io's Copy directly
// in the build pipeline file would create a circular-looking
// dependency back to runtime utilities. Local to this package.
func copyFileForWrap(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
