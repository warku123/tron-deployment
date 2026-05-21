# trond build — quickstart

Copy/pasteable dev-loop for building java-tron from source and
deploying the result through the same `trond apply` flow you'd use
for a pre-built image.

Everything below assumes you already have `trond` installed (see
the project [README](../../README.md)) and a `java-tron` checkout
on disk.

## TL;DR

```bash
# 1. Build a fat JAR from java-tron's master branch.
trond build \
  --source /path/to/java-tron \
  --artifact jar -o json

# 2. Deploy a node that consumes that JAR (intent.yaml has a build: block).
trond apply --intent ./my-node.yaml -o json

# 3. Survey the cache.
trond build list
trond build inspect <cache-key>
trond build prune --older-than 168h --confirm   # 7 days
```

The build pipeline is content-addressed: same source + same builder
image + same task + same args → same `cache_key` → instant cache hit
on the second invocation. No artifact is ever re-built unnecessarily.

## Builder choice — docker vs host

trond ships two builder backends. Pick whichever fits your machine.

| | `--builder docker` (default) | `--builder host` |
|---|---|---|
| Toolchain | Pinned `eclipse-temurin:<jdk>-jdk-jammy` image | Your local `java` + the source's `./gradlew` |
| Reproducibility | High — same digest across machines | Only as reproducible as your host JDK |
| Requirements | Docker daemon reachable | `java` on PATH; `./gradlew` in the source tree |
| `artifact: jar` | ✓ | ✓ |
| `artifact: image` (either strategy) | ✓ | ✓ (image step still uses host docker) |
| Cross-arch builds | `--platform linux/amd64 \| linux/arm64` via QEMU | Host arch only |
| Cache key includes | Pinned builder image digest | sha256 of `java -version` output |
| First-build time (java-tron) | ~5–10 min cold (longer under QEMU), ~50 ms cache hit | ~3–8 min cold, ~50 ms cache hit |

Both builders need a reachable docker daemon for image artifacts —
the `jar-wrap` strategy runs `docker build` host-side, and the
`gradle` strategy invokes gradle's docker plugin which talks to the
local daemon either directly (host) or via a `docker.sock` mount
(docker builder).

You can switch builders freely — they produce distinct cache entries
(the BuilderImageDigest differs), so a stray host build won't
clobber your reproducible docker artifacts.

## Walkthrough — local deploy with a build block

### 1. Write the intent

```yaml
# my-node.yaml
name: my-dev-fullnode
network: nile
target:
  type: local
  runtime: jar
nodes:
  - type: fullnode
    install_path: /tmp/trond-dev/my-dev-fullnode
    resources:
      memory: 4G
    build:
      source: ../java-tron     # relative to THIS file (FR-021)
      builder: docker          # or "host"
      jdk: "17"                # optional; auto-detects from platform
      gradle_task: ":framework:buildFullNodeJar"   # stock java-tron
```

Relative `build.source` paths resolve against the intent file's
directory, mirroring `docker-compose`'s `build.context` convention.

### 2. Preflight

```bash
trond preflight --intent my-node.yaml
```

You'll see two groups of checks:

```
✓ docker               Docker version 27.4.1
✓ disk                 12GB free
✓ memory               16GB total
✓ port-8090            Port 8090 (http) available
…
✓ build-git            git version 2.43.0
✓ build-source-java-tron  /path/to/java-tron
✓ build-docker-local   docker server 27.4.1
```

The `build-*` checks run on the LOCAL machine (where the build will
execute), regardless of `target.type`. SSH targets get target-side
checks AND local-side build checks.

### 3. Apply

```bash
trond apply --intent my-node.yaml -o json
```

First invocation: ~3–5 minutes for the gradle compile.
Second invocation against an unchanged source: ~50 ms cache hit.

The output's `build` field surfaces the cache state:

```json
{
  "name": "my-dev-fullnode",
  "result": "created",
  "build": {
    "cache_key": "abc12345-bdeadbef+dirty-7f2a3b9c-xa1b2c3d4",
    "source_revision": "8f4e2a3c1234567890abcdef1234567890abcdef",
    "dirty": true,
    "artifact_path": "/home/you/.trond/builds/out/abc12345-….jar",
    "sha256": "160185bf2867…",
    "cache_hit": false,
    "duration_ms": 215000
  },
  "endpoints": {"http": "http://127.0.0.1:8090", "grpc": "127.0.0.1:50051"}
}
```

`artifact_path` lives under `$TROND_STATE_DIR/builds/out/` (or
`~/.trond/builds/out/` if you haven't overridden `--state-dir` /
`$TROND_STATE_DIR`).

`dirty: true` means your working tree had uncommitted edits; trond
folded a patch hash into the cache key so unrelated edits don't
share the same artifact. `dirty: false` means the resolved revision
matches `HEAD` exactly.

## SSH target — build locally, deploy remotely

trond never executes builds on the deploy target. With
`target.type: ssh`, the build runs on your laptop and the resulting
JAR is `scp`'d to the remote `install_path`.

```yaml
target:
  type: ssh
  host: 10.0.0.42
  user: ubuntu
  identity_file: ~/.ssh/id_ed25519
  runtime: jar
nodes:
  - type: fullnode
    install_path: /home/ubuntu/trond
    process_manager: nohup       # or systemd if you have sudo
    build:
      source: ../java-tron
      builder: host              # or docker; same cache either way
```

`trond apply` will:

1. Run the build locally (or hit cache).
2. SHA256 the remote's existing JAR, if any — skip the transfer when
   it matches.
3. Stream the JAR via SSH to `<install_path>/FullNode.jar.tmp`.
4. `mv` atomically to the final path (no half-written file is ever
   visible to the running node).
5. Install a systemd unit / nohup launcher per `process_manager`.
6. Start the node.

The SHA256 fast-path makes subsequent applies essentially free: no
bytes leave your laptop if the remote already holds the right JAR.

## Image artifact — build a docker image, not a JAR

Two strategies, picked via `build.image_strategy`:

### `gradle` (default) — for source trees that ship a `dockerBuild` task

```yaml
build:
  source: ../java-tron
  artifact: image
  image_tag: my-fork/java-tron:dev
  # image_strategy: gradle   ← implicit default
```

trond invokes gradle's docker plugin inside the builder container,
diff-snapshots `docker images` before/after, and tags the new image
as `<image_tag>` for compose to consume.

### `jar-wrap` — for stock java-tron (no `dockerBuild` task)

```yaml
build:
  source: ../java-tron
  artifact: image
  image_strategy: jar-wrap
  image_tag: my-fork/java-tron:dev
  gradle_task: ":framework:buildFullNodeJar"   # produces the JAR
  platform: linux/amd64                        # optional; omit = host arch
```

trond builds the fat JAR via the standard Phase 1 path, then runs
`docker build` against an embedded Dockerfile that COPYs the JAR
into a pinned `eclipse-temurin:<jdk>-jdk-jammy` runtime with
`tcmalloc_minimal` preloaded (matches upstream `tron-docker`'s
patterns).

The wrapped image is cross-arch safe — setting `build.platform`
(e.g. `linux/amd64` on an arm64 host) produces a working image for
the target arch without docker.sock trickery (the inner JAR is JVM
bytecode, the outer step is COPY-only). Omit `platform:` to get
host arch.

## Patching source before build

For workflows where you need to compile a *modified* java-tron — testing
an unmerged upstream PR, backporting a fix to an older version, adding
instrumentation for stress / replay testing, security or compliance
variants — declare the patches in your intent and trond handles the rest:

```yaml
build:
  source: ../java-tron
  gradle_task: ":framework:buildFullNodeJar"
  patches:
    - ../tron-deployment/tools/replay/patches/01-skip-tx-expiration.patch
    - ../tron-deployment/tools/replay/patches/02-skip-tapos-validation.patch
    - ../tron-deployment/tools/replay/patches/03-skip-network-expiration.patch
    - ../tron-deployment/tools/replay/patches/04-fast-proposals.patch
```

Paths are intent-relative (same rule as `build.source`). What trond does
when `patches:` is non-empty:

1. Validates each path: file exists, looks like a unified diff (catches the
   common mistake of pointing at a YAML or a JAR by accident).
2. For each patch, records `(basename, sha256(file-contents))`. Computes
   `patch_hash` as a deterministic fold of those per-patch SHA256s in
   declared order — a **pure function of patch contents**. Same intent +
   same patches → same cache key on every machine. The per-patch records
   land in the build manifest so `trond build inspect <key>` surfaces a
   portable fingerprint (basename + content sha256) rather than absolute
   filesystem paths that drift when patches move.
3. Opens a fresh `git worktree` at `$TROND_STATE_DIR/builds/worktrees/<cache-key>/`,
   checks out your `build.revision` (or HEAD) there, and `git apply`s the
   patches in declared order.
4. Runs gradle against the **worktree**, not your primary source. Your
   own `git status` stays untouched — IDE windows don't churn, WIP edits
   are safe.
5. Removes the worktree after the build completes, success or failure.
   Cache hits skip steps 3-5 entirely.

Cache reuse across machines is now real: a teammate cloning this intent
gets bit-identical artifacts from the cache. Compare against today's
dirty-tree path, where every operator's local `.DS_Store` or IDE temp
file shifted the cache key.

### Authoring patches

A patch is just a unified diff. Easiest way to produce one:

```bash
cd /path/to/java-tron
# Make your changes; ./gradlew test as needed
git diff > ~/my-feature.patch        # all changes
git diff path/to/specific/File.java  # one file only
# Then `git checkout -- .` to restore the working tree; trond will
# re-apply the patch when it builds.
```

Then reference `~/my-feature.patch` from `build.patches`. trond uses
`git apply --check` before mutation, so a patch that doesn't apply
cleanly fails fast with `PATCH_FAILED` pointing at the offending file
and `git apply`'s stderr.

### When NOT to use patches

For iterative dev where you're actively editing the source and rebuilding
every few seconds, **skip `patches:`**. Today's working-tree-dirty path
still works: just edit the source, run `trond apply`, get the artifact
with your edits. `PatchHash` falls back to `git diff` semantics and the
build runs in your source dir (no worktree overhead).

The declarative `patches:` field is for **systematic, reproducible**
patching — when "this artifact needs these N patches" is part of the
deploy's truth.

## Cache management

### List

```bash
trond build list
trond build list --filter image --sort size
trond build list --include-orphans -o json | jq '.entries[] | select(.orphaned)'
```

Each row shows cache_key, kind, source_revision (short), size,
created_at, and the artifact path or image tag. Newest-first by
default; `--sort size` finds the biggest cache hogs.

### Inspect

```bash
trond build inspect 260585c9397b    # unambiguous prefix is enough
trond build inspect 260585c9397b-bd0861e68 -o json
```

Returns the full manifest plus size + orphan state. On an ambiguous
prefix you get a `AMBIGUOUS_PREFIX` error listing the candidates.

For entries built with `build.patches`, the JSON output includes a
`patches` array of `{name, sha256}` records:

```jsonc
{
  "cache_key": "...",
  "patches": [
    {"name": "01-skip-tx-expiration.patch",
     "sha256": "a1b2c3...8 chars... ...64 hex total"},
    {"name": "02-skip-tapos-validation.patch",
     "sha256": "..."}
  ],
  ...
}
```

To verify your local patches still match this cached artifact (e.g.
on a teammate's machine), compare the on-disk sha256 of each local
patch file against the manifest's records — same content = same key.

### Prune

```bash
# Dry-run (default): see the plan before doing anything destructive.
trond build prune --older-than 168h

# Actually delete (note: --confirm is REQUIRED to delete).
trond build prune --older-than 168h --confirm

# Safety-net combo: keep the 3 newest regardless of age.
trond build prune --older-than 168h --keep-last 3 --confirm

# Wipe orphans only (artifacts deleted out-of-band; manifests left behind).
trond build prune --orphan --confirm

# Nuclear option — wipe everything.
trond build prune --all --confirm
```

Filters AND together; `--keep-last N` is a global safety net (the N
newest entries are protected, even if the other filters would
target them). `--keep-last N` alone with `--confirm` is rejected —
combine with `--all` (explicit ack) or `--orphan` / `--older-than`
to scope the deletion.

For image entries, prune also runs `docker image rm --force <tag>`
so the docker storage layer actually reclaims layers.

## MCP usage

The build cache tools are exposed over MCP for chat-based agents:

```jsonc
// Tool: build_list
// Args: { "filter": "all|jar|image", "sort": "newest|oldest|size", "include_orphans": false }
// Returns: { "count": N, "entries": [...] }

// Tool: build_inspect
// Args: { "cache_key": "<full or unambiguous prefix>" }
// Returns: single entry (same shape as build_list entries)

// Tool: build_prune                     ← carries destructiveHint
// Args: { "all": false, "older_than": "168h", "keep_last": 3,
//         "orphan_only": false, "confirm": false }
// Returns: { "dry_run": bool, "plan": [...], "removed": [...], "freed_bytes": N }
```

Build *execution* (`trond build`) is intentionally NOT exposed via
MCP — it's a long-running, stderr-streaming operation whose progress
is best surfaced via the CLI's `-o json` stream. Agents that need
to drive a build call the CLI binary directly.

## Troubleshooting

**`Java 17 is required for aarch64`** — java-tron's build.gradle
enforces a strict platform/JDK matrix (amd64 → JDK 8, arm64 → JDK
17). trond warns on mismatched combinations but doesn't block;
gradle does. Either set `build.jdk` to match or accept the warning
and let trond pick the default.

**`gradle wrapper not present`** — `--builder host` requires the
source tree to ship `./gradlew`. Run `gradle wrapper` once in your
checkout, or switch to `--builder docker`.

**`docker: cannot overwrite digest`** — cross-arch build pulled a
multi-arch manifest list digest; the per-platform digest is what
trond's pin schema needs. This was a real bug fixed in Phase 3;
file an issue if you hit it on current main.

**Stale cache after a JDK upgrade** — host-builder cache keys
include the sha256 of `java -version`. After a JDK install upgrade,
the old entries become orphaned (their digest no longer matches the
host); `trond build prune --orphan --confirm` cleans them up.

## Reference

- `trond build --help` — full flag reference
- `trond schema build -o json` — machine-readable contract
- [spec.md](./spec.md) — functional requirements (FR-001 through FR-024)
- [plan.md](./plan.md) — design rationale + phase-by-phase implementation plan
- [AGENTS.md](../../AGENTS.md) — agent-facing workflows
