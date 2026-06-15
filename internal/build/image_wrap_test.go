package build

import (
	"reflect"
	"strings"
	"testing"
)

// TestJarReqForWrap pins the recursive-Run contract: image_strategy=
// jar-wrap calls Run() with a derived JAR-shaped Request, and that
// inner Request MUST be the same shape an explicit `--artifact jar`
// invocation produces — otherwise the two paths populate distinct
// cache entries and the wrap path forces a JAR rebuild every time.
//
// Also covers the lock-ordering invariant (outer image-key flock →
// inner jar-key flock): if the inner Request accidentally kept
// ImageStrategy/ArtifactKind=image set, Run would recurse back
// into buildImageJarWrap → double-acquire the image-key lock →
// deadlock. The clears below prevent that.
func TestJarReqForWrap(t *testing.T) {
	t.Run("clears image-only fields", func(t *testing.T) {
		outer := Request{
			SourcePath:    "/some/src",
			RevisionSpec:  "HEAD",
			ArtifactKind:  "image",
			ImageTag:      "trond-build/java-tron:dev",
			ImageStrategy: "jar-wrap",
			JDKVersion:    "17",
			Platform:      "linux/arm64",
			GradleTask:    "dockerBuild", // the artifact=image default
		}
		got := jarReqForWrap(outer)

		if got.ArtifactKind != "jar" {
			t.Errorf("ArtifactKind = %q; want jar", got.ArtifactKind)
		}
		if got.ImageTag != "" {
			t.Errorf("ImageTag = %q; want empty (no image step on inner JAR build)", got.ImageTag)
		}
		if got.ImageStrategy != "" {
			t.Errorf("ImageStrategy = %q; want empty (inner Run must NOT recurse into jar-wrap → deadlock)", got.ImageStrategy)
		}
		if got.GradleTask != "shadowJar" {
			t.Errorf("GradleTask = %q; want shadowJar (dockerBuild default swapped to JAR default)", got.GradleTask)
		}
		// Source/revision/platform/JDK MUST pass through unchanged so
		// the inner JAR cache key matches what an explicit
		// `--artifact jar` invocation produces.
		if got.SourcePath != outer.SourcePath ||
			got.RevisionSpec != outer.RevisionSpec ||
			got.JDKVersion != outer.JDKVersion ||
			got.Platform != outer.Platform {
			t.Errorf("source/revision/jdk/platform must pass through unchanged; got %+v", got)
		}
	})

	t.Run("preserves explicit GradleTask override", func(t *testing.T) {
		outer := Request{
			ArtifactKind:  "image",
			ImageStrategy: "jar-wrap",
			GradleTask:    ":framework:buildFullNodeJar", // user override
		}
		got := jarReqForWrap(outer)
		if got.GradleTask != ":framework:buildFullNodeJar" {
			t.Errorf("explicit GradleTask was clobbered: %q", got.GradleTask)
		}
	})

	t.Run("patches flow through to inner JAR build", func(t *testing.T) {
		// Review-pass-8 H1 guard: when an image-strategy=jar-wrap
		// build declares patches, those patches MUST land in the
		// inner JAR request so the wrapped image carries patched
		// bytecode. A bug that cleared Patches alongside ImageTag /
		// ImageStrategy would silently produce a wrapped image
		// with UN-patched code — the worst kind of regression,
		// because the build succeeds and the image runs.
		outer := Request{
			SourcePath:    "/some/src",
			ArtifactKind:  "image",
			ImageStrategy: "jar-wrap",
			ImageTag:      "tag:dev",
			Patches: []string{
				"/abs/p1.patch", "/abs/p2.patch",
			},
		}
		got := jarReqForWrap(outer)
		if len(got.Patches) != 2 ||
			got.Patches[0] != "/abs/p1.patch" ||
			got.Patches[1] != "/abs/p2.patch" {
			t.Errorf("Patches must propagate to inner JAR build unchanged; got %v", got.Patches)
		}
	})

	t.Run("equivalent to explicit jar request", func(t *testing.T) {
		// The crux of the cache-reuse invariant: building source S
		// twice — once with --artifact jar, once with --artifact
		// image --image-strategy jar-wrap — must produce identical
		// JAR Requests (modulo trivially-equal fields). Else the JAR
		// cache holds two entries for the same logical artifact.
		explicit := Request{
			SourcePath:   "/some/src",
			RevisionSpec: "HEAD",
			ArtifactKind: "jar",
			JDKVersion:   "17",
			Platform:     "linux/arm64",
			GradleTask:   "shadowJar",
		}
		derived := jarReqForWrap(Request{
			SourcePath:    "/some/src",
			RevisionSpec:  "HEAD",
			ArtifactKind:  "image",
			ImageStrategy: "jar-wrap",
			ImageTag:      "any:tag",
			JDKVersion:    "17",
			Platform:      "linux/arm64",
			GradleTask:    "dockerBuild",
		})
		if !reflect.DeepEqual(explicit, derived) {
			t.Errorf("explicit and derived JAR Requests must match for cache reuse:\n  explicit: %+v\n  derived:  %+v", explicit, derived)
		}
	})
}

// TestArchTripletForPlatform pins the platform → Debian multi-arch
// triplet mapping. The Dockerfile's LD_PRELOAD path embeds the
// triplet, so a typo here = broken tcmalloc at runtime = silent
// performance regression.
func TestArchTripletForPlatform(t *testing.T) {
	cases := []struct {
		platform string
		want     string
	}{
		{"linux/amd64", "x86_64-linux-gnu"},
		{"linux/arm64", "aarch64-linux-gnu"},
		{"", "x86_64-linux-gnu"},              // default
		{"linux/ppc64le", "x86_64-linux-gnu"}, // unsupported → safe fallback
	}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			if got := archTripletForPlatform(tc.platform); got != tc.want {
				t.Errorf("archTripletForPlatform(%q) = %q; want %q",
					tc.platform, got, tc.want)
			}
		})
	}
}

// TestJarWrapDockerfileTemplate_HasAllPlaceholders enforces that
// every placeholder image_wrap.go substitutes is actually present
// in the embedded template. If someone adds a new placeholder to
// the rendering code but forgets to use it in the Dockerfile (or
// vice versa — adds a {{X}} to the Dockerfile that nobody
// substitutes), trond's `docker build` would either fail or leak a
// literal `{{X}}` into the image.
func TestJarWrapDockerfileTemplate_HasAllPlaceholders(t *testing.T) {
	required := []string{
		"{{BASE_IMAGE}}",
		"{{JAR_NAME}}",
		"{{ARCH_TRIPLET}}",
		"{{SOURCE_REVISION}}",
		"{{CACHE_KEY}}",
		"{{BUILD_TIME}}",
	}
	for _, ph := range required {
		t.Run(ph, func(t *testing.T) {
			if !strings.Contains(jarWrapDockerfileTemplate, ph) {
				t.Errorf("template missing placeholder %q; image_wrap.go won't have anything to substitute", ph)
			}
		})
	}

	// Reverse direction: no leftover {{...}} should remain after a
	// stub substitution. Catches template typos (e.g. {{BASE-IMAGE}}
	// or {{JarName}}) that escape unit tests above.
	rendered := strings.NewReplacer(
		"{{BASE_IMAGE}}", "stub-image:latest@sha256:0",
		"{{JAR_NAME}}", "FullNode.jar",
		"{{ARCH_TRIPLET}}", "x86_64-linux-gnu",
		"{{SOURCE_REVISION}}", "0000000000000000000000000000000000000000",
		"{{CACHE_KEY}}", "stub-key",
		"{{BUILD_TIME}}", "2026-01-01T00:00:00Z",
	).Replace(jarWrapDockerfileTemplate)
	if strings.Contains(rendered, "{{") {
		// Find and report what's left.
		idx := strings.Index(rendered, "{{")
		end := strings.Index(rendered[idx:], "}}")
		var ctx string
		if end > 0 {
			ctx = rendered[idx : idx+end+2]
		}
		t.Errorf("Dockerfile template has unsubstituted placeholder: %s", ctx)
	}
}

// TestJarWrapDockerfile_HasTcmalloc is the Phase 5d.1 regression
// guard: the upstream tron-docker image relies on tcmalloc to keep
// java-tron's allocator pressure manageable under sustained RPS.
// If a future template refactor drops this, java-tron runs but
// perf regresses silently. This test pins it.
func TestJarWrapDockerfile_HasTcmalloc(t *testing.T) {
	checks := []string{
		"libtcmalloc-minimal4",     // apt-get install
		"libtcmalloc_minimal.so.4", // LD_PRELOAD path
		"LD_PRELOAD",
		"TCMALLOC_RELEASE_RATE",
	}
	for _, c := range checks {
		t.Run(c, func(t *testing.T) {
			if !strings.Contains(jarWrapDockerfileTemplate, c) {
				t.Errorf("template missing %q — tcmalloc setup broken", c)
			}
		})
	}
}

// TestJarWrapDockerfile_HasOCILabels enforces that the OCI label
// set tron-docker users expect (and downstream tooling like
// `docker inspect` / Grafana / etc. depends on) is present.
func TestJarWrapDockerfile_HasOCILabels(t *testing.T) {
	required := []string{
		"org.opencontainers.image.title",
		"org.opencontainers.image.source",
		"org.opencontainers.image.revision",
		"org.opencontainers.image.created",
		"trond.cache_key",
	}
	for _, label := range required {
		t.Run(label, func(t *testing.T) {
			if !strings.Contains(jarWrapDockerfileTemplate, label) {
				t.Errorf("Dockerfile missing OCI label %q", label)
			}
		})
	}
}
