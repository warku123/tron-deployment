# Feature Specification: trond Build Pipeline

**Feature Branch**: `feat/build-pipeline`
**Created**: 2026-05-08
**Last revised**: 2026-05-08 (self-review pass — see CHANGELOG below)
**Status**: Draft
**Input**: User description: "Add a `trond build` capability that produces deployable
java-tron artifacts (JAR or Docker image) from a source tree, integrated with
`trond apply` so a developer can iterate on java-tron code and redeploy with a
single command."

## Background

trond today consumes pre-built java-tron artifacts: an official Docker image
(`tronprotocol/java-tron:<tag>`) or a pre-built JAR. The "edit java-tron code,
test the change on a node" loop is unsupported — operators must context-switch
to tron-docker / java-tron's Gradle toolchain to produce a custom artifact,
then hand-stitch the result back into a trond intent.

This feature closes that loop. trond gains a `build` verb that orchestrates a
containerized Gradle invocation and an `apply`-side hook that resolves a
`build:` block in intent.yaml automatically.

## Non-Goals

trond will NOT implement Java compilation, dependency resolution, or the Gradle
DSL itself. The Java toolchain runs unchanged inside a container that trond
manages; trond is an orchestrator, not a re-implementation.

trond will NOT replace tron-docker's release-grade image build (signed,
SBOM-attached, multi-arch matrix). That stays in tron-docker's CI. trond's
`build` targets the development inner loop.

## Clarifications

### Session 2026-05-08

- Q: Should the builder image be reproducible or follow upstream? → A: Pin a
  specific sha256 digest per JDK version. Bump the pin in a trond release.
- Q: Keep a `--builder host` escape hatch for developers with local gradle? →
  A: Yes — same flags, skips the container path. Low maintenance cost.
- Q: How should SSH targets handle the build step? → A: Build locally, scp the
  resulting JAR to the remote target, start the node there. Don't ship source.
- Q: Is `trond build` a standalone command or only an apply-side phase? →
  A: Both. Standalone for CI / debugging; apply-side for the integrated loop.

### Session 2026-05-08 (self-review pass)

- Q: Should `build.rebuild` field exist (`always | on_change | never`)? →
  A: **Removed.** Semantics overlapped confusingly with revision = HEAD +
  dirty detection. The cache-key derivation already handles the legitimate
  cases; an explicit field added zero value.
- Q: Patch hash for dirty trees: just `git diff`? → A: **No.** `git diff`
  misses untracked files, which would silently cache-hit a stale artifact.
  Patch hash MUST combine `git diff` and `git status --porcelain -uall`
  (so a brand-new `.java` file invalidates the cache).
- Q: Configurable gradle task name? → A: Yes, as `build.gradle_task` field
  with sensible defaults (`shadowJar` for jar, `dockerBuild` for image).

### Session 2026-05-08 (review pass 2)

- Q: How to pass gradle_task / gradle_args without shell injection? →
  A: Never invoke `bash -c`. Pass gradle task and args as separate
  `exec.Command` argv; validate each token against
  `^[a-zA-Z0-9._:=/+-]+$` at parse time and reject otherwise.
- Q: Should the cache key incorporate the builder image digest? → A: Yes.
  Cache key becomes `<git-sha>-b<digest-prefix>` so a pin bump silently
  invalidates stale artifacts. Same for `+dirty-` variants.
- Q: `build.env` arbitrary keys? → A: **No.** v1 is an allowlist: env
  passthrough is restricted to `GRADLE_OPTS`, `JAVA_OPTS`,
  `GRADLE_USER_HOME`, `MAVEN_OPTS`, and any var matching
  `ORG_GRADLE_PROJECT_*`. Extending the list is a code change, not an
  intent change.
- Q: How is `builder_image_digests.json` distributed? → A: Embedded into
  the trond binary via `go:embed`. Pin bumps ship with releases. A
  `--builder-image-override <ref@sha256:...>` escape hatch exists for
  emergencies but is not documented in the dev-loop quickstart.
- Q: Source path relative to what? → A: CLI `--source ./path` resolves
  relative to CWD. `build.source: ./path` in intent resolves relative
  to the intent file's directory (matches docker-compose `build.context`).
- Q: Should cache hit verify the artifact file exists on disk? → A: Yes.
  Manifest existence is necessary but not sufficient; cache lookup MUST
  stat the artifact and treat a missing file as a miss.
- Q: Should SIGINT cancel scp transfers too? → A: Yes. The whole apply
  flow runs under the same signal-aware context. scp writes to a
  `.tmp` suffix and renames on success so a cancelled transfer leaves
  no half-written artifact on the remote.

### Session 2026-05-08 (review pass 3)

- Q: `gradle_args` validation by character class? → A: **No.** Character
  regex was both too tight (rejects `,` in `--projects=a,b,c`, spaces in
  `-Dtitle=my title`) and too loose (allows `--init-script
  /tmp/evil.gradle`, which is the actual threat). Switched to a
  **flag-name allowlist**: `--offline`, `--no-daemon`, `--parallel`,
  `--max-workers=N`, `--rerun-tasks`, `-D<key>=<val>`, `-P<key>=<val>`,
  `-q`/`-i`/`-d`. The value portion is unrestricted (argv form is
  already shell-safe). `gradle_task` continues to use a tight char
  regex (`^[a-zA-Z][a-zA-Z0-9:_-]*$`) since task names are inherently
  regular.
- Q: Windows support for concurrent-build serialization? → A: trond
  publishes a windows/amd64 binary, but `syscall.Flock` is POSIX-only.
  Implementation split via `//go:build !windows` — Windows uses an
  in-process mutex only (no cross-process protection). FR-015 caveat
  records this. Doc says: concurrent `trond build` from two trond
  processes is undefined on Windows.
- Q: `image_tag` format validation? → A: Validated against Docker
  reference format. `image_tag: /etc/passwd` and similar must be
  rejected with `VALIDATION_ERROR` at intent parse time.
- Q: `state.json` field naming — `build_revision` or `build_cache_key`?
  → A: **`build_cache_key`.** The stored value is the full cache key
  (`<sha>-b<digest>[+dirty-<patch>]`), not just a git revision. Renaming
  pre-implementation so we don't ship the misleading name.
- Q: What if a pinned builder image digest becomes unreachable? → A:
  Fail loudly. `error_code: "BUILDER_IMAGE_UNAVAILABLE"`, suggestions
  point at `--builder-image-override` and "upgrade trond." Do NOT
  silently fall back to unpinned tags — that defeats reproducibility.
- Q: Does `build prune` clean up docker image layer disk usage? → A:
  Yes. For `--artifact image`, prune runs `docker image rm <id>` (not
  just `docker untag`). Layers shared with other images stay because
  docker handles refcounting.
- Q: Audit log atomicity around long-running builds? → A: Append a
  `result: "in_progress"` event at build start; update atomically to
  the terminal state on completion. A crash mid-build leaves an
  `in_progress` entry visible via `trond events`, surfacing the
  forensic signal.

## User Scenarios & Testing

### User Story 1 — Build a JAR from local source (Priority: P1)

A java-tron developer has cloned the source, modified a few files, and wants a
fat JAR they can hand to `trond apply` or run separately. They invoke
`trond build` against the source directory and receive a JSON response
containing the artifact path, source revision, and content hash.

**Why this priority**: This is the foundation. Until a single artifact can be
produced repeatably from a source tree, nothing else in this feature works.

**Independent Test**:
```bash
trond build --source ./java-tron --artifact jar -o json
```
Delivers a JAR file under `~/.trond/builds/out/` and a JSON manifest under
`~/.trond/builds/manifest/`.

**Acceptance Scenarios**:

1. **Given** a clean java-tron working tree at revision `abc123`, **When** the
   developer runs `trond build --source ./java-tron --artifact jar -o json`,
   **Then** trond pulls the pinned `eclipse-temurin:8-jdk` image (first run
   only), runs `./gradlew shadowJar` inside it, and emits
   `{"source_revision":"abc123", "artifact_path":"~/.trond/builds/out/abc123.jar",
   "sha256":"...", "duration_ms":..., "cache_hit": false}`.

2. **Given** the same revision built once already, **When** the developer
   re-runs the same command, **Then** trond returns the cached manifest with
   `cache_hit: true` and `duration_ms < 200`. The on-disk JAR is byte-identical.

3. **Given** uncommitted edits in the working tree **and** an untracked new
   file, **When** the developer runs the same command, **Then** trond
   computes a patch hash that combines `git diff` and `git status --porcelain
   -uall`, names the artifact `abc123+dirty-<patchsha>.jar`, and treats it as
   a distinct cache entry. Adding an untracked file MUST change the patch
   hash (regression guard against the v1 design bug found in self-review).

4. **Given** the build fails (compile error in user's code), **When** the
   command runs, **Then** trond surfaces a structured error envelope
   (`error_code: "BUILD_FAILED"`, message containing gradle's tail output,
   `suggestions` pointing at common causes) and exits 1.

5. **Given** Docker is not running on the host, **When** the developer
   invokes `trond build` without `--builder host`, **Then** trond exits with
   `error_code: "TARGET_UNREACHABLE"`, exit code 3, and a suggestion to start
   Docker or pass `--builder host`.

6. **Given** the user sends SIGINT during a running build, **When** trond
   receives the signal, **Then** trond kills the build container, removes
   any partial output from `out/`, leaves no manifest entry for the aborted
   build, and exits with `error_code: "BUILD_CANCELLED"`, exit code 130
   (standard for SIGINT).

7. **Given** two concurrent `trond build` invocations against the same
   source + revision, **When** they race, **Then** the second caller
   acquires a per-key file lock, waits for the first to complete, and
   returns a cache hit (no duplicate build).

### User Story 2 — Build and deploy in one command (Priority: P1)

A developer has an intent file that references a `build:` block instead of a
prebuilt image. They run `trond apply --intent dev.yaml`. trond resolves the
build first, then deploys a node against the freshly produced artifact.

**Why this priority**: This is the dev loop trond is being extended for. Story
1 alone leaves the build/deploy boundary manual.

**Independent Test**:
```bash
trond apply --intent examples/dev-local.yaml --auto-approve --wait -o json
```
Delivers a running node whose runtime points at the just-built artifact.

**Acceptance Scenarios**:

1. **Given** an intent with `build.source: ./java-tron, build.revision: HEAD,
   build.artifact: jar`, **When** `apply` runs and the revision has not been
   built before, **Then** trond invokes the build pipeline, then proceeds to
   `apply`. The JSON output contains both a `build` block and the usual `apply`
   fields.

2. **Given** the intent points at a build that's already cached, **When**
   `apply` runs and the node is already deployed at that same revision,
   **Then** the result is `no_change` and total duration is < 5 seconds.

3. **Given** the build succeeds but the resulting JAR is not a valid
   java-tron fat JAR (no `org.tron.program.FullNode` main class), **When**
   `apply` reaches the runtime step, **Then** trond fails with `error_code:
   "INVALID_ARTIFACT"`, NOT with a runtime crash inside the container.

4. **Given** `runtime: docker` with `artifact: image`, **When** trond renders
   the compose file, **Then** the service has `pull_policy: never` set, so
   compose does not attempt to pull the locally-built tag from a remote
   registry.

### User Story 3 — Build a Docker image, not a JAR (Priority: P2)

The same developer wants a runnable Docker image rather than a JAR (so the
node uses the docker runtime). They set `build.artifact: image` and a
`build.image_tag` in their intent.

**Independent Test**:
```bash
trond build --source ./java-tron --artifact image --tag trond-dev:abc123 -o json
```

**Acceptance Scenarios**:

1. **Given** a tron-docker-shaped source tree (gradle target `dockerBuild`),
   **When** the build runs, **Then** trond invokes the gradle docker plugin
   and produces a local image tagged `trond-dev:abc123`. The manifest records
   the image ID (sha256 digest).

2. **Given** the artifact is `image`, **When** `apply` runs, **Then** the
   rendered docker-compose references the local image tag directly with
   `pull_policy: never`.

### User Story 4 — Deploy over SSH using a locally built JAR (Priority: P2)

A developer builds on their laptop and deploys to a remote Linux VM via SSH.

**Independent Test**:
```bash
trond apply --intent examples/dev-ssh.yaml -o json
```
where the intent has both a `build:` block AND `target.type: ssh`.

**Acceptance Scenarios**:

1. **Given** a successful local build of `<sha>.jar`, **When** `apply` runs
   against an SSH target, **Then** trond scp's the JAR to the remote host's
   deployment directory, configures the systemd unit to point at it, and
   starts the service. No source is shipped to the remote.

2. **Given** the remote target already has the same `<sha>.jar` on disk (from
   a prior run), **When** `apply` runs, **Then** trond skips the transfer
   step and goes directly to the start phase.

3. **Given** a slow link and a large fat JAR (~200 MB), **When** the
   transfer starts, **Then** trond emits MCP progress notifications (same
   mechanism `snapshot download` uses) so a connected agent surfaces a
   live progress bar to the user.

4. **Given** the SSH target host lacks `scp` (some hardened distros), **When**
   preflight runs, **Then** trond reports a clear `error_code:
   "TARGET_MISSING_TOOL"` with a suggestion to install openssh-clients on
   the remote.

### User Story 5 — Use host gradle (no container) (Priority: P3)

A developer with a configured local Gradle daemon wants to skip the container
for max-iteration speed.

**Acceptance Scenarios**:

1. **Given** `--builder host` is passed (CLI) or `build.builder: host` is set
   (intent), **When** the build runs, **Then** trond invokes `./gradlew` from
   the source directory directly. JDK + Gradle version mismatches surface as
   `BUILD_FAILED` with `suggestions` pointing at the required versions.

### Edge Cases

- **Source tree on a separate filesystem with weird permissions**: trond
  mounts read-only so the build can't corrupt source. Gradle wrapper attempts
  to write to `~/.gradle/caches` — trond redirects this via mount.
- **Out-of-disk during a build**: detected by gradle's own error; trond
  surfaces with a disk-space suggestion.
- **Concurrent `trond build` calls on the same revision**: per-key
  `flock`-based serialization (FR-015); second caller waits then returns cache
  hit.
- **Builder image not present and offline**: surface clear network-error
  message, suggest `--builder host` fallback.
- **Revision is `HEAD` but git working tree is not a git repo**: error
  `error_code: "INVALID_SOURCE"` with suggestion to either commit or pass an
  explicit `--source-id <name>`.
- **Source path is a symlink**: resolve it before mounting; Docker's mount
  resolves symlinks at mount-time, so trond canonicalizes the path first.
- **Untracked file added between builds**: the patch hash MUST include
  untracked files (FR-002 explicit), preventing stale cache hits.

## Requirements

### Functional Requirements

- **FR-001**: trond MUST expose a `trond build` cobra command accepting at
  minimum `--source <path>`, `--artifact <jar|image>`, `--revision <rev>`,
  `--jdk <version>`, `--builder <docker|host>`, `--tag <tag>` (for image),
  `--gradle-task <name>` (override default), `--gradle-arg <flag>`
  (repeatable; e.g. `--gradle-arg=--offline`).
- **FR-002**: The build cache MUST be content-addressed. The cache key
  MUST combine: resolved git sha, builder image digest prefix (so a pin
  bump silently invalidates stale artifacts), jdk version, artifact kind,
  gradle task name, and, for dirty working trees, a patch hash computed
  over the COMBINED output of `git diff` AND `git status --porcelain
  -uall` (so untracked files invalidate cache). On-disk naming:
  `<git-sha>-b<digest6>[+dirty-<patchsha8>].(jar|imgmeta)`.
- **FR-003**: Build outputs MUST live under `${TROND_STATE_DIR}/builds/`
  (default `~/.trond/builds/`) and respect `--state-dir` / `TROND_STATE_DIR`.
- **FR-004**: Each completed build MUST produce a JSON manifest matching
  `schemas/output/build.schema.json`.
- **FR-005**: The intent.yaml schema MUST accept a `build:` block that is
  mutually exclusive with `image:` and references source + revision. When
  `runtime: docker` + `artifact: image`, the rendered compose service MUST
  carry `pull_policy: never`. `build.image_tag` MUST match Docker's
  reference format (validated via `github.com/distribution/reference` or
  equivalent regex); paths, whitespace, and uppercase are rejected with
  `VALIDATION_ERROR`.
- **FR-006**: `trond apply` MUST resolve a `build:` block by invoking the
  build pipeline before the render/deploy phases.
- **FR-007**: A build failure MUST set exit code 1 and `error_code:
  "BUILD_FAILED"`, with the gradle stderr tail (last ~50 lines) in the
  message. Output of a successful build MUST be silent on stdout in `-o
  json` mode; verbose mode streams gradle output to stderr.
- **FR-008**: The default builder MUST be `docker`. The host builder MUST be
  available as a flag override.
- **FR-009**: When the target is SSH, trond MUST build locally and transfer
  the JAR via scp. Source code MUST NOT be shipped to the remote. Transfers
  > 50 MB MUST emit MCP progress notifications.
- **FR-010**: Builds MUST be discoverable: `trond build list`, `trond build
  prune --keep N`, `trond build inspect <sha>`.
- **FR-011**: trond MUST validate the produced artifact is structurally valid
  (JAR contains `org.tron.program.FullNode` main class, or image has runnable
  ENTRYPOINT) before declaring success.
- **FR-012**: The pinned builder image MUST be reproducible: trond ships a
  `builder_image_digests.json` mapping `jdk_version → sha256:...`. Upgrading
  the pin is a trond release change, not a runtime change. A `make
  refresh-builder-pins` target MUST regenerate the file from current Temurin
  tags so digest drift is a one-command bump.
- **FR-013**: All build-related operations MUST be exposed through MCP as
  tools so AI agents can drive the dev loop. Tool annotations:
  - `build`: `idempotentHint=true`, `destructiveHint=false`
  - `build_list`, `build_inspect`: `readOnlyHint=true`
  - `build_prune`: `destructiveHint=true`
- **FR-014**: The audit log MUST record each build invocation (revision,
  duration, result) alongside the existing apply/upgrade events.
- **FR-015**: Concurrent `trond build` invocations against the same cache
  key MUST serialize via a per-key `flock` on
  `${TROND_STATE_DIR}/builds/locks/<key>.lock`. The waiting caller MUST
  return a cache hit after the first completes (not duplicate work).
  On Windows (no POSIX `flock`), the implementation falls back to an
  in-process mutex only; cross-process serialization is undefined and
  documented as such. Goal: don't break the Windows build.
- **FR-016**: `trond build` AND the build phase of `trond apply` MUST run
  under a signal-aware context. SIGINT MUST terminate the build container
  AND any in-flight scp transfer, remove partial output (`out/*.tmp`,
  remote `*.tmp` files), omit the manifest entry, and exit 130 with
  `error_code: "BUILD_CANCELLED"`.
- **FR-017**: `trond preflight --intent <yaml>` MUST, when the intent
  contains a `build:` block, additionally verify: (a) source path exists
  and is a git repo, (b) docker is reachable (or host gradle exists with
  `--builder host`), (c) the builder image is in the local cache (a
  network-reachable warm-pull check runs only when the image is missing;
  offline hosts get a warning, not an error), (d) for SSH targets, scp
  is present on the remote.
- **FR-018**: `trond build prune` MUST cross-reference `state.json` and
  refuse to delete any build whose cache key equals the
  `build_cache_key` field of a currently-managed node. The state schema
  gains an optional `build_cache_key` field on each node entry
  (additive, MINOR bump). For `--artifact image` entries, prune MUST
  call `docker image rm <image_id>` (not just `docker untag`) so layer
  storage is actually released; docker's refcounting protects layers
  shared with other tags.
- **FR-019**: The build environment MUST forward env vars to the build
  container ONLY from a fixed allowlist: `GRADLE_OPTS`, `JAVA_OPTS`,
  `GRADLE_USER_HOME`, `MAVEN_OPTS`, and any var matching the
  `ORG_GRADLE_PROJECT_*` prefix. Intent-side `build.env: { KEY: VALUE }`
  is also restricted to this allowlist; unknown keys MUST fail validation
  with `VALIDATION_ERROR`. Extending the allowlist is a trond code
  change, not an intent change.
- **FR-020**: Cache lookup MUST stat the manifest's referenced artifact
  (jar file or local image tag) and treat a missing artifact as a cache
  miss. Manifests pointing at missing artifacts MUST be removed during
  the next prune.
- **FR-021**: Source path resolution: `--source ./path` on the CLI
  resolves relative to CWD; `build.source: ./path` in intent.yaml
  resolves relative to the intent file's parent directory (matching
  docker-compose's `build.context` convention).
- **FR-022**: All gradle invocations MUST use a no-shell argv form
  (`exec.Command("docker", "run", ..., image, "./gradlew", task,
  args...)` — never `bash -c "..."`). Validation:
  - `gradle_task` MUST match `^[a-zA-Z][a-zA-Z0-9:_-]*$` (task names
    are inherently regular). Examples accepted: `shadowJar`,
    `:dbfork:build`. Rejected: anything with whitespace, `;`, `$()`,
    or path separators.
  - `gradle_args` is restricted by a **flag-name allowlist**, not a
    character class. Accepted: `--offline`, `--no-daemon`, `--parallel`,
    `--max-workers=<int>`, `--rerun-tasks`, `-D<key>=<val>`,
    `-P<key>=<val>`, `-q` / `-i` / `-d`. The value portion of `-D`/`-P`
    is unrestricted (argv form is already shell-safe). Any other
    flag, including `--init-script`, `--include-build`, `--build-file`,
    `--settings-file`, is rejected with `VALIDATION_ERROR` because they
    can redirect the build to attacker-supplied logic.
- **FR-023**: Each build event in the audit log MUST conform to:
  `{timestamp, command: "build", result:
  "in_progress"|"success"|"failed"|"cancelled", build:
  {source_revision, dirty: bool, jdk_version, artifact_kind, builder,
  duration_ms, error_code: string|null}}`. Lifecycle: append a
  `result: "in_progress"` event at build start, then update atomically
  to the terminal result on completion. A trond process crash mid-build
  leaves an `in_progress` entry visible via `trond events`, surfacing
  the forensic signal. Schema is shared with the existing audit-log
  shape.
- **FR-024**: `builder_image_digests.json` MUST be embedded into the
  trond binary via `go:embed`. Runtime override via
  `--builder-image-override <ref@sha256:...>` is allowed but is an
  escape hatch (not promoted in the dev-loop quickstart). Override values
  participate in the cache key (FR-002) so they don't pollute pinned
  caches. When the pinned digest is unreachable (image removed from
  registry, network outage of the registry itself), trond MUST exit
  with `error_code: "BUILDER_IMAGE_UNAVAILABLE"`, `exit_code: 3`, and
  surface suggestions covering both `--builder-image-override <ref>`
  and "upgrade trond." trond MUST NOT silently fall back to an
  unpinned tag — that defeats reproducibility.
- **FR-025**: The dirty-build cache TTL MUST be user-configurable via
  `build.cache.dirty_ttl` in intent (or `--cache-dirty-ttl` on `build
  prune`). Default 7 days. Accepted: any Go `time.ParseDuration` value
  plus the literal `never`.

- **FR-026**: The intent's `build:` block MUST accept an optional
  `patches: []string` field listing paths to unified-diff files
  (`*.patch`) to be applied to the resolved source revision before
  gradle runs. Path resolution mirrors FR-021 (intent-relative).
  When `patches` is non-empty:
  - Cache key derivation MUST use `patch_hash = sha256(canonical
    concat of patch file contents in declared order)` — a pure
    function of inputs, NOT the working tree's `git diff`. This
    guarantees two operators with the same intent and the same
    patch files produce the same cache key regardless of their
    local working tree state.
  - The build MUST run in an isolated `git worktree` checked out at
    the resolved revision so patches apply against a known-clean
    tree. The worktree path is `${TROND_STATE_DIR}/builds/worktrees/
    <cache-key>/`. It is created on cache miss and removed after
    build (success or failure) — cache hits never touch the
    worktree.
  - Patches are applied via `git apply --check` (validation) then
    `git apply` (mutation). A patch that fails to apply MUST surface
    as a `PATCH_FAILED` structured error with the offending patch
    path and `git apply`'s stderr, exit code 2.
  - The user's primary source tree is NEVER mutated. Existing
    iterative dev-loop behavior (run `trond build` against a dirty
    working tree) is preserved when `patches` is absent.

- **FR-027**: When `patches: []` is empty/absent, build pipeline
  behavior is unchanged from FR-001..025: the build runs in the
  user's source tree directly, `patch_hash` is computed from
  `git diff` of the working tree (today's semantics, machine-local).
  This preserves backward compatibility for every existing intent
  file.

- **FR-028**: Each patch file referenced by `build.patches` MUST be
  validated at intent-load time:
  - Path exists and is a regular file.
  - File starts with a unified-diff header (`diff --git ` or
    `--- ` / `+++ ` lines) to catch the common error of pointing at
    a non-patch file.
  - Validation errors surface as `INVALID_PATCH` with the path that
    failed validation, exit code 2.

### Key Entities

- **Build**: A content-addressed compilation of a java-tron source tree.
  Properties: source_revision (git sha), patch_hash (if dirty OR if
  patches: was set), builder_image_digest (which JDK image produced
  this), jdk_version, artifact_kind (jar|image), artifact_ref (path
  or image tag), sha256/image_id, duration_ms, builder (docker|host),
  gradle_task, gradle_args, patches (list of paths, when present),
  created_at.
- **Source**: A reference to a java-tron checkout. Properties: path
  (canonicalized), revision_spec (HEAD|branch|tag|sha), resolved_revision,
  dirty_state (boolean), patch_hash (when dirty_state OR when
  build.patches was declared — see FR-026).
- **Worktree** (FR-026): A trond-managed `git worktree` at
  `${TROND_STATE_DIR}/builds/worktrees/<cache-key>/`. Lifecycle:
  created on cache miss when `build.patches` is non-empty, populated
  with the resolved revision + applied patches, used as the gradle
  source dir, removed after build completion (success OR failure).
  Cache hits skip the worktree entirely. Git's object DB is shared
  with the parent checkout, so each worktree's disk overhead is the
  working tree only, not the full repo.
- **Builder Image Pin**: A frozen mapping `jdk_version → image@sha256:...`
  bundled with each trond release. Refresh path: `make
  refresh-builder-pins`.
- **State node entry** (existing, extended): adds optional
  `build_cache_key: string` field (full cache key, not just git sha),
  populated by apply when the deploy consumed a `build:` block. Used by
  `build prune` to refuse deletion of in-use builds.

### Success Criteria

- **SC-001**: A developer with java-tron source on their laptop can run
  `trond apply --intent dev.yaml` and reach a running node in < 5 minutes for
  a cold build, < 1 minute for a cached build.
- **SC-002**: Re-running `trond apply` against an unchanged source tree
  results in `no_change` and exits within 2 seconds.
- **SC-003**: A build can be triggered by an AI agent through MCP (the
  `build` tool) and the agent can chain build → apply → status in one
  conversation.
- **SC-004**: The first 10 trond users to try the dev-loop quickstart (US-1
  / US-2) complete it without manual intervention on the build step.

## Out of Scope (For This Feature)

- Multi-arch image build matrix (`linux/amd64,linux/arm64`). Recorded in
  follow-up. The Phase 4 design notes the buildx hook point.
- Build provenance signing (cosign-signed artifacts at build time). The
  release pipeline in tron-docker already does this; trond's dev builds are
  intentionally unsigned.
- Builds against branches of dependencies (e.g., custom protobuf-java).
- Cross-source builds (combine multiple repos into one artifact).
- Remote-host builds (build *on* the SSH target). Out of scope per
  clarification.
- Image artifact + SSH target combined. Use registry push for that case
  (existing `image:` path).

## Dependencies

- This feature does NOT depend on the toolkit wrapper or analyze layer.
- The shadow-fork feature DOES depend on a working build pipeline (it needs
  to produce a forked-state JAR/image).

## CHANGELOG

- **2026-05-08**: Initial draft.
- **2026-05-08 (self-review pass 1)**: Applied 17-item review. Material
  changes:
  - Removed ambiguous `build.rebuild: always|on_change|never` field.
  - FR-002 patch hash now MUST include untracked files (regression bug
    fix in design).
  - FR-005 makes `pull_policy: never` explicit for local-built images.
  - FR-015 (concurrent lock), FR-016 (SIGINT), FR-017 (preflight),
    FR-018 (prune cross-ref state), FR-019 (env passthrough) all newly
    added.
  - US-1 gained acceptance scenarios 6-7 (SIGINT, concurrent).
  - US-2 gained scenario 4 (pull_policy: never).
  - US-4 gained scenarios 3-4 (progress, scp probe).
  - FR-013 MCP tool annotations now explicit.
- **2026-05-08 (self-review pass 3)**: Applied 10-item third review.
  - **Portability bug**: FR-015 now documents Windows fallback for
    `flock` (in-process mutex only; cross-process serialization
    undefined). Prevents windows/amd64 build break.
  - **Defense correction**: FR-022 swaps the `gradle_args` char-regex
    for a flag-name allowlist (`--init-script /tmp/evil.gradle` was
    passing the old regex while legitimate `--projects=a,b,c` was
    failing it). `gradle_task` keeps its tight char-regex since task
    names are inherently regular.
  - **Input validation**: FR-005 grows an `image_tag` reference-format
    check (rejects `image_tag: /etc/passwd` etc.).
  - **State naming**: `build_revision` field renamed to
    `build_cache_key` — the stored value is the cache key
    (`<sha>-b<digest>[+dirty-<patch>]`), not just a git sha.
  - **Resilience**: FR-024 specifies behavior when a pinned digest
    becomes unreachable — explicit error, no silent fallback to
    unpinned tags.
  - **Resource cleanup**: FR-018 prune now calls `docker image rm`
    (not `docker untag`) so image layer storage is actually freed.
  - **Audit lifecycle**: FR-023 introduces `result: "in_progress"`
    appended at build start, atomically updated on completion. A
    crashed build leaves an inspectable forensic entry.
- **2026-05-08 (self-review pass 2)**: Applied 12-item second review.
  Material changes:
  - **Security**: FR-022 forbids shell-mediated gradle invocations
    (closes command-injection via `gradle_task` / `gradle_args`); FR-019
    narrows `build.env` from "any KEY" to a fixed allowlist (closes
    `LD_PRELOAD`-style hijacks). Token regex added to `--gradle-task`
    and `--gradle-arg`.
  - **Correctness**: FR-002 cache key now includes builder image digest
    (pin bump invalidates stale artifacts); FR-020 makes cache hit also
    verify artifact file exists on disk.
  - **Distribution**: FR-024 makes the pin file `go:embed`-ed so the
    binary is the source of truth, with `--builder-image-override` as
    documented escape hatch.
  - **UX**: FR-021 disambiguates `source:` relative path resolution
    (CLI = CWD, intent = intent-file dir).
  - **Robustness**: FR-016 extends SIGINT handling to scp; uses
    `.tmp` + rename so remote never sees half-written JARs.
    FR-017 builder-image preflight is offline-friendly (warning on
    missing-and-offline, not hard fail).
  - **Audit/observability**: FR-023 fixes the audit-log build event
    JSON shape so tooling can rely on it.
  - **Configurability**: FR-001 grows `--gradle-arg <flag>` repeatable;
    FR-025 makes dirty-cache TTL user-tunable (default 7d).
