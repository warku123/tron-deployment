// Package schema exposes the JSON Schema files that document trond's
// JSON output shapes, plus a cobra-tree walker that produces a
// machine-readable manifest of the whole CLI surface.
//
// Agents call `trond schema -o json` once at session startup, parse
// the manifest, and from then on know every command, every flag, every
// expected output field. They never need to read --help text.
package schema

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// SchemaVersion is the trond CLI contract version. Semver semantics:
//
//   - PATCH: a single existing schema gains an additive optional field
//     (clients that ignore unknown fields are unaffected).
//   - MINOR: an entirely new schema is added (a new command becomes
//     manifest-discoverable; existing schemas unchanged).
//   - MAJOR: an existing field is renamed, removed, or its meaning
//     shifts. Agents pinned to the prior major may break.
//
// Agents should pin to MAJOR for compat detection and re-read AGENTS.md
// when MAJOR bumps. The version bump rationale is also captured in
// CHANGELOG entries.
//
// History:
//
//	1.0.0 — initial 21 schemas (apply, plan, status, list, inspect,
//	        diagnose, health, verify, preflight, doctor, version,
//	        events, config-validate, config-render, network-create,
//	        network-status, snapshot-sources, snapshot-list,
//	        snapshot-download, snapshot-jobs, error envelope).
//	1.1.0 — add recipe-list / recipe-show / recipe-run schemas (no
//	        changes to existing schemas).
//	1.2.0 — add verify-config + auto-heal schemas (new `trond
//	        verify-config` and `trond auto-heal` commands).
//	1.3.0 — add build schema (new `trond build` command + intent
//	        `build:` block; state node entry gains optional
//	        `build_cache_key`).
//	1.4.0 — add build-list / build-inspect / build-prune schemas
//	        (new `trond build list/inspect/prune` cache mgmt
//	        subcommands; existing schemas unchanged).
//	1.5.0 — add shadow-fork-mutate schema (new `trond shadow-fork
//	        mutate` command — Go port of java DbFork; existing
//	        schemas unchanged).
//	1.6.0 — add monitoring schema (new `trond apply --monitor` flag
//	        and intent `monitoring:` block; new `network-create`
//	        `--monitor` flag; apply.output gains `monitoring_error`
//	        and `monitoring` fields).
//	1.7.0 — add `patches` optional field to build entry schemas
//	        (build-list, build-inspect, build-prune) and to the
//	        Manifest. Additive — old clients ignoring unknown
//	        fields are unaffected. Drives FR-026 (declarative
//	        source patches via build.patches).
const SchemaVersion = "1.7.0"

// JSONSchemaURLBase is the canonical URL prefix for individual output
// schema files. Embedded $id values inside each schema mirror this so
// online and offline validation both work.
const JSONSchemaURLBase = "https://github.com/tronprotocol/tron-deployment/blob/master/schemas/output/"

//go:embed files/*.json
var schemaFS embed.FS

// rawSchemas reads every embedded *.json once at startup, parses each
// into a generic map for round-tripping, and indexes them by short
// name (the basename minus ".schema.json"). Failure is fatal — an
// invalid embedded JSON is a build defect, not a runtime concern.
var rawSchemas = func() map[string]map[string]any {
	out := map[string]map[string]any{}
	entries, err := fs.ReadDir(schemaFS, "files")
	if err != nil {
		panic("schema: cannot read embedded files dir: " + err.Error())
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		data, err := fs.ReadFile(schemaFS, "files/"+e.Name())
		if err != nil {
			panic("schema: cannot read " + e.Name() + ": " + err.Error())
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			panic("schema: cannot parse " + e.Name() + ": " + err.Error())
		}
		key := strings.TrimSuffix(e.Name(), ".schema.json")
		out[key] = doc
	}
	return out
}()

// Names returns the short names of every embedded schema, sorted.
func Names() []string {
	names := make([]string, 0, len(rawSchemas))
	for k := range rawSchemas {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Get returns the parsed schema by short name (e.g. "apply",
// "snapshot-download"), or false if no schema is registered for that
// command. The returned map is safe to mutate by the caller — it's a
// fresh copy for each request.
func Get(name string) (map[string]any, bool) {
	doc, ok := rawSchemas[name]
	if !ok {
		return nil, false
	}
	return cloneMap(doc), true
}

// URLFor returns the canonical $schema URL for a command's output. It
// matches the `$id` embedded inside the schema file itself.
func URLFor(name string) string {
	return JSONSchemaURLBase + name + ".schema.json"
}

// cloneMap deep-copies a JSON-decoded map so callers can't accidentally
// pollute the embedded schemas at runtime. Only handles the value types
// json.Unmarshal produces (no chan/func/etc.), which is sufficient.
func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		dup := make([]any, len(x))
		for i, item := range x {
			dup[i] = cloneValue(item)
		}
		return dup
	default:
		return x // strings, numbers, bools, nil are immutable
	}
}

// Errorf is a tiny helper used by the schema cobra command to report
// "no schema for that name" with the same suggestions[] convention as
// the rest of trond's error envelope.
func Errorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
