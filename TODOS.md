# TODOS

Deferred work captured during reviews. Each item carries enough context to pick
up cold.

## Public `trond snapshot clone` verb

- **What:** Expose the `internal/fsclone` copy-on-write clone primitive as a
  user-facing `trond snapshot clone <src> <dst>` CLI verb.
- **Why:** Lets agents / operators build a warm pool of fixtures interactively —
  clone a cached, pinned snapshot to a fresh isolated dir in seconds instead of
  re-downloading or re-copying 30–90 GB.
- **Pros:** Completes the warm-pool story as a first-class capability; useful for
  the "a new agent just uses trond" workflow.
- **Cons:** Speculative public API with only one consumer today (the equivalence
  test). Adds CLI surface, docs, and a test matrix for a capability nobody is
  asking for interactively yet.
- **Context:** Deferred during the 2026-06-13 `/plan-eng-review` (Issue 1). The
  reusable primitive lives in `internal/fsclone` (`x/sys/unix` clonefile/FICLONE
  with byte-copy fallback). The outside-voice (Codex) Net agreed: don't broaden
  to a public verb until a second real consumer exists AND CI proves CoW is
  available where it matters.
- **Depends on / blocked by:** `internal/fsclone` primitive landing (this PR);
  a concrete second consumer (e.g. an agent warm-pool flow or `apply --snapshot`).
