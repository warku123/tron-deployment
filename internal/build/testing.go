package build

import "context"

// TestRunner is the abstract signature callers outside this package
// substitute for the real docker-shelling-out runner. Mirrors the
// internal buildRunner interface. Lives in a non-_test.go file so
// it's accessible from other packages' tests (which can't import
// _test files), but kept on a narrow surface.
//
// `outDir` is the host-side per-artifact directory the runner is
// expected to populate (image-kind builds write
// <cache-key>-images-{before,after} snapshot files there; jar-kind
// builds use outTmpPath). Test fakes for image artifacts MUST honor
// outDir — internal/build/image.go reads the snapshots from there
// to identify the produced image; a stub that drops outDir silently
// makes the image-build path untestable.
type TestRunner interface {
	RunDockerBuild(ctx context.Context, sourcePath, outDir, outTmpPath string, gradleTask string, gradleArgs []string, env map[string]string) error
}

// SetTestRunner swaps the package-level runner with a caller-supplied
// stub for the duration of the returned restore func. Intended for
// tests in other packages (internal/apply, cmd/, MCP) that need to
// drive Run() without an actual docker daemon. Returns a restore
// func that the caller defers.
//
// Example:
//
//	restore := build.SetTestRunner(myStub)
//	defer restore()
//	build.Run(ctx, req)
func SetTestRunner(stub TestRunner) (restore func()) {
	orig := defaultRunner
	defaultRunner = &adapter{stub: stub}
	return func() { defaultRunner = orig }
}

type adapter struct {
	stub TestRunner
}

// RunBuild satisfies the internal buildRunner interface by
// translating the new method shape down to the exported TestRunner's
// signature. Passes the effective build source dir (r.buildSourceDir,
// which is the worktree when build.patches was set, else the user's
// source path) so test stubs see what the real runners see — tests
// that exercise the patches path can assert on the worktree path.
// outDir flows through so image-kind test stubs can write their
// image-id snapshot files where image.go expects them.
func (a *adapter) RunBuild(ctx context.Context, r *resolved, outDir, outTmp string) error {
	return a.stub.RunDockerBuild(ctx, r.buildSourceDir, outDir, outTmp,
		r.req.GradleTask, r.req.GradleArgs, r.req.Env)
}
