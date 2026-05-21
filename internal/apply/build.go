package apply

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tronprotocol/tron-deployment/internal/build"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// transferBuiltJAR is the Phase 4 SSH-target hook: scp the locally-
// built JAR to the remote install path, with a sha256 fast-path that
// skips the transfer when the remote already holds the bit-identical
// artifact.
//
// Atomicity is delegated to target.PutFile (which writes
// `<remotePath>.tmp` then atomically renames). SIGINT during the
// transfer is honored via the inherited ctx; the SSH target's
// implementation cleans up the `.tmp` on the remote.
//
// Source-tree paranoia (spec/002): no source bytes ever leave the
// host. Only the artifact ships.
func transferBuiltJAR(
	ctx context.Context,
	tgt target.Target,
	summary *BuildSummary,
	localPath, remotePath string,
) error {
	// Fast path: same sha256 → no transfer needed. Saves the
	// round-trip and the bandwidth on intent re-applies that didn't
	// change the source.
	//
	// On a transport-level Sha256IfExists error (SSH session can't
	// open, connection dropped) we bail immediately rather than waste
	// a multi-hundred-MB PutFile attempt on the same broken link —
	// PutFile would just hit the same failure with a longer trace.
	// Exit-code failures (file missing, sha256sum absent) come back
	// as ("", nil) and correctly fall through to PutFile.
	if summary != nil && summary.SHA256 != "" {
		remoteSHA, err := tgt.Sha256IfExists(ctx, remotePath)
		if err != nil {
			return output.NewErrorf("DEPLOY_ERROR", output.ExitGeneralError,
				"check remote sha256 for %s: %s", remotePath, err.Error()).
				WithSuggestions(
					"Verify the remote target is reachable: trond preflight --intent <intent>",
				)
		}
		if remoteSHA == summary.SHA256 {
			return nil
		}
	}
	if err := tgt.PutFile(ctx, localPath, remotePath); err != nil {
		return output.NewErrorf("DEPLOY_ERROR", output.ExitGeneralError,
			"transfer built JAR to %s: %s", remotePath, err.Error()).
			WithSuggestions(
				"Verify the remote target is reachable: trond preflight --intent <intent>",
				"Verify the remote install_path is writable by the SSH user",
			)
	}
	return nil
}

// resolveBuild handles the optional `build:` block on a node. When
// present it invokes the build pipeline (cache-hit-fast-path) and
// returns the resolved artifact plus a summary slice for the apply
// result envelope. Returns (nil, "", "", nil) when the node has no
// build block — the caller proceeds with the legacy image / jar
// source paths.
//
// Per spec/002 FR-021, a relative `build.source` resolves against
// the intent file's directory (matches docker-compose's
// `build.context` convention). The CLI's `--source` already resolves
// against CWD before constructing intent.Build, so this function
// only needs to handle the intent path.
func resolveBuild(
	ctx context.Context,
	opts Options,
	node *intent.NodeSpec,
) (summary *BuildSummary, builtJarPath, builtImageTag string, err error) {
	if node.Build == nil {
		return nil, "", "", nil
	}
	bs := node.Build

	source, srcErr := resolveBuildSource(bs.Source, opts.IntentPath)
	if srcErr != nil {
		return nil, "", "", output.NewErrorf("INVALID_SOURCE",
			output.ExitValidationError, "%s", srcErr.Error())
	}

	// Resolve patch paths intent-relative — mirror build.source's
	// FR-021 semantics so the operator can put patches next to the
	// intent file (or in a sibling repo like tron-deployment's
	// tools/replay/patches/).
	patches := make([]string, 0, len(bs.Patches))
	for _, p := range bs.Patches {
		resolved, perr := resolveBuildSource(p, opts.IntentPath)
		if perr != nil {
			return nil, "", "", output.NewErrorf("INVALID_PATCH",
				output.ExitValidationError, "patch %q: %s", p, perr.Error())
		}
		patches = append(patches, resolved)
	}

	req := build.Request{
		SourcePath:           source,
		RevisionSpec:         bs.Revision,
		JDKVersion:           bs.JDK,
		ArtifactKind:         bs.Artifact,
		ImageTag:             bs.ImageTag,
		GradleTask:           bs.GradleTask,
		GradleArgs:           append([]string(nil), bs.GradleArgs...),
		Builder:              bs.Builder,
		BuilderImageOverride: bs.BuilderImageOverride,
		Env:                  bs.Env,
		Platform:             bs.Platform,
		ImageStrategy:        bs.ImageStrategy,
		Patches:              patches,
	}

	res, runErr := build.Run(ctx, req)
	if runErr != nil {
		// build.Run returns *output.StructuredError on user-facing
		// failure paths; propagate that directly so the wire envelope
		// is preserved.
		return nil, "", "", runErr
	}

	summary = &BuildSummary{
		CacheKey:       res.CacheKey,
		SourceRevision: res.SourceRevision,
		Dirty:          res.Dirty,
		ArtifactPath:   res.ArtifactPath,
		ImageTag:       res.ImageTag,
		SHA256:         res.SHA256,
		BuilderImage:   res.BuilderImage,
		Platform:       res.Platform,
		JDKVersion:     res.JDKVersion,
		CacheHit:       res.CacheHit,
		DurationMs:     res.DurationMs,
	}

	switch res.ArtifactKind {
	case "jar":
		builtJarPath = res.ArtifactPath
	case "image":
		// Phase 3 wires this into the docker runtime path. Phase 2
		// leaves the field populated so apply can record it but the
		// runtime switch ignores it for now.
		builtImageTag = res.ImageTag
	default:
		return nil, "", "", output.NewErrorf("INTERNAL_ERROR",
			output.ExitGeneralError,
			"build returned unknown artifact_kind %q", res.ArtifactKind)
	}
	return summary, builtJarPath, builtImageTag, nil
}

// resolveBuildSource implements FR-021's intent-relative path
// resolution. Absolute paths pass through unchanged. Relative paths
// resolve against the intent file's parent directory. If the intent
// was supplied via stdin or an in-memory source (IntentPath empty),
// the relative path is treated as relative to CWD as a fallback.
func resolveBuildSource(source, intentPath string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("build.source is required")
	}
	if filepath.IsAbs(source) {
		return filepath.Clean(source), nil
	}
	if intentPath != "" {
		base := filepath.Dir(intentPath)
		return filepath.Clean(filepath.Join(base, source)), nil
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve relative source %q: %w", source, err)
	}
	return abs, nil
}
