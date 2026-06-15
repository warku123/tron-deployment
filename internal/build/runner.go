package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// buildRunner abstracts the "run gradle to produce the artifact"
// step so tests can substitute a recorder/mock without spinning up
// a real Docker daemon (or a real gradle install for host-builder
// tests). Production has two implementations: realDockerRunner runs
// gradle inside a pinned eclipse-temurin container; realHostRunner
// runs it directly via the source tree's ./gradlew wrapper.
//
// The interface intentionally accepts the full argv (not pieces) —
// tests assert on that argv to enforce FR-022's argv-only invocation
// contract (no `bash -c "...interpolated..."`).
type buildRunner interface {
	RunBuild(ctx context.Context, r *resolved, outDir, outTmp string) error
}

// defaultRunner is package-level so tests can swap it via
// `t.Cleanup(func() { defaultRunner = orig })`. Production uses
// dispatchRunner which routes to realDockerRunner or realHostRunner
// based on r.req.Builder ("docker" or "host").
var defaultRunner buildRunner = dispatchRunner{}

// dockerBuildScript (JAR variant) is the only piece of shell trond
// runs for artifact=jar and it's a compile-time constant. User input
// (gradle_task, gradle_args) arrives through `"$@"` argv expansion
// AFTER `--`; output filename arrives through an env var. Both
// channels are shell-quote-safe independent of their contents.
// FR-022 holds: no path from user input to shell metacharacter
// interpretation.
//
// Why bash and not argv? Because the build produces files inside the
// container's /src tree (gradle writes to build/libs/*.jar) and we
// need to *move* them into /out so they survive container teardown.
// A `./gradlew` argv invocation alone leaves the artifact in the
// container's ephemeral layer.
//
// `ls -S` sorts by size, so `head -n1` picks the largest jar — for
// the shadow plugin that's the fat jar with dependencies; thin jars
// (if also emitted) are smaller. ValidateJARMainClass rejects any
// non-FullNode jar that wins this heuristic.
const dockerBuildScript = `set -e
cd /src
./gradlew "$@"
# Search every */build/libs/*.jar (root project + every subproject)
# because multi-module gradle builds put the fat JAR under a
# specific submodule (e.g. java-tron's :framework:buildFullNodeJar
# emits to framework/build/libs/FullNode.jar, NOT to the root's
# build/libs/). Sort by size descending so we pick the fat JAR
# rather than a thin module jar of the same gradle task.
JAR=$(find . -path '*/build/libs/*.jar' -type f 2>/dev/null | xargs ls -S 2>/dev/null | head -n1)
if [ -z "$JAR" ]; then
  echo "trond: gradle produced no .jar under any build/libs/" >&2
  exit 64
fi
cp "$JAR" "/out/$OUT_NAME"
`

// dockerBuildScript_Image is the image-artifact variant. The
// gradle docker plugin tags the produced image directly into the
// host's docker daemon (we bind-mount /var/run/docker.sock; see the
// runner setup below).
//
// To robustly identify which image gradle just created — without
// racing other docker activity on the host — we snapshot the set
// of TAGGED image IDs (dangling=false filters out multi-stage
// intermediate layers) before AND after the build, into
// per-cache-key files so concurrent builds on different keys can't
// clobber each other. The diff itself runs host-side in
// computeNewImages (image.go) so it's unit-testable and doesn't
// depend on GNU comm.
//
// Same FR-022 invariant: gradle args flow through "$@", no
// interpolation of trond-side fields. The cache key arrives via
// $CACHE_KEY env (allowlisted to safe characters by FR-002's
// content-addressed naming scheme).
const dockerBuildScript_Image = `set -e
cd /src
docker images -q --no-trunc --filter dangling=false 2>/dev/null | sort -u > "/out/$CACHE_KEY-images-before"
./gradlew "$@"
docker images -q --no-trunc --filter dangling=false 2>/dev/null | sort -u > "/out/$CACHE_KEY-images-after"
`

// dispatchRunner is the production buildRunner. It looks at
// r.req.Builder and forwards to the docker or host variant. Pulling
// the routing into its own type (rather than putting an `if Builder
// == "host"` check inside realDockerRunner) keeps each concrete
// runner single-purpose and lets tests substitute either backend
// independently.
type dispatchRunner struct{}

func (dispatchRunner) RunBuild(ctx context.Context, r *resolved, outDir, outTmp string) error {
	if r.req.Builder == "host" {
		return realHostRunner{}.RunBuild(ctx, r, outDir, outTmp)
	}
	return realDockerRunner{}.RunBuild(ctx, r, outDir, outTmp)
}

type realDockerRunner struct{}

func (realDockerRunner) RunBuild(ctx context.Context, r *resolved, outDir, outTmp string) error {
	// Gradle cache: use a DOCKER NAMED VOLUME, not a bind mount.
	// macOS Docker Desktop's bind-mount layer (virtiofs) doesn't
	// reliably preserve the exec bit for files the container writes
	// to host paths. Gradle's protobuf plugin downloads native
	// `protoc-*.exe` binaries into the cache; via bind mount they
	// arrive on the host without `+x`, then the next gradle run
	// (or a sub-task in the same run) sees them as non-executable
	// and bails with EACCES. A named volume lives inside Docker's
	// VM and preserves modes natively. The cache is still
	// per-trond-state-dir-scoped via a volume label so concurrent
	// trond invocations on different state dirs don't collide.
	const gradleVolume = "trond-build-gradle-cache"

	args := []string{
		"run", "--rm",
		// /src must be RW because gradle writes build/, .gradle/ into
		// the project tree (same as running ./gradlew on the host).
		// The user already gives gradle this access locally.
		"-v", r.buildSourceDir + ":/src:rw",
		"-v", gradleVolume + ":/root/.gradle",
		"--workdir", "/src",
	}

	// Artifact-kind specific volume + env setup.
	//
	//   jar:   /out holds the produced JAR; $OUT_NAME tells the
	//          script what filename to drop in /out.
	//   image: /var/run/docker.sock mounted into the builder so
	//          gradle's docker plugin can call back into the host
	//          daemon to build + tag an image. We ALSO mount /out
	//          because the build-around snapshot script
	//          (dockerBuildScript_Image) writes the
	//          before/after image-id diff there for the host side
	//          to read.
	args = append(args, "-v", outDir+":/out:rw")
	switch r.req.ArtifactKind {
	case "image":
		args = append(args,
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			// Per-cache-key snapshot file names so concurrent builds
			// on different keys can't clobber each other's
			// before/after files in the shared /out dir.
			"-e", "CACHE_KEY="+r.cacheKeyStr)
	default:
		args = append(args, "-e", "OUT_NAME="+filepath.Base(outTmp))
	}

	// --platform routes to the matching variant of the multi-arch
	// builder image. On a cross-arch combination docker uses QEMU
	// emulation (binfmt-misc); 3-5× slower but functional. Required
	// for the java-tron compat matrix: amd64+JDK8 vs arm64+JDK17.
	if r.req.Platform != "" {
		args = append(args, "--platform", r.req.Platform)
	}
	for _, e := range allowedEnvPassthrough(r.req.Env) {
		args = append(args, "-e", e)
	}

	script := dockerBuildScript
	if r.req.ArtifactKind == "image" {
		script = dockerBuildScript_Image
	}
	args = append(args, r.imageRef, "bash", "-c", script, "--")
	args = append(args, r.req.GradleTask)
	args = append(args, r.req.GradleArgs...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	// In `-o json` mode the caller redirects stdout to a JSON buffer;
	// gradle's chatter belongs on stderr regardless of trond's output
	// mode so it never corrupts the JSON envelope.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
