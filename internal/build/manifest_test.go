package build

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestManifest_PatchRecordRoundTrip pins the wire shape of
// Manifest.Patches []PatchRecord. Without this, a future field-tag
// typo would silently drop or rename `name` / `sha256` on disk,
// breaking `trond build inspect` consumers and any agent that
// validates against schemas/output/build-inspect.schema.json.
//
// The earlier review (pass 6) gap-flagged that no test exercised
// the JSON serialization of the new shape — this closes it.
func TestManifest_PatchRecordRoundTrip(t *testing.T) {
	original := &Manifest{
		CacheKey:       "abc12345-bdeadbeef",
		SourcePath:     "/some/src",
		SourceRevision: "0123456789abcdef0123456789abcdef01234567",
		Dirty:          true,
		BuilderImage:   "eclipse-temurin:17-jdk-jammy",
		JDKVersion:     "17",
		ArtifactKind:   "jar",
		GradleTask:     "shadowJar",
		Builder:        "docker",
		Patches: []PatchRecord{
			{Name: "01-skip-tx-expiration.patch",
				SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{Name: "02-skip-tapos-validation.patch",
				SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		CreatedAt: time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC),
	}

	wire, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(wire)

	// Field-name guards. If these names change, the schema files
	// MUST also change — keeps Manifest <-> build-*.schema.json
	// from drifting silently.
	for _, want := range []string{
		`"patches":`,
		`"name":"01-skip-tx-expiration.patch"`,
		`"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		`"name":"02-skip-tapos-validation.patch"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wire JSON missing %q\nfull body:\n%s", want, body)
		}
	}

	var roundtrip Manifest
	if err := json.Unmarshal(wire, &roundtrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(roundtrip.Patches, original.Patches) {
		t.Errorf("Patches did not round-trip cleanly\n  original:  %+v\n  roundtrip: %+v",
			original.Patches, roundtrip.Patches)
	}
}

// TestManifest_NilPatchesOmitted pins the `omitempty` behavior:
// a manifest without patches MUST NOT emit `"patches": null` (which
// would force schema consumers to handle the null case) or
// `"patches": []` (which is misleading — empty array implies "we
// tried to patch but had zero patches" rather than "no patching
// requested").
func TestManifest_NilPatchesOmitted(t *testing.T) {
	m := &Manifest{
		CacheKey:     "x",
		ArtifactKind: "jar",
		GradleTask:   "shadowJar",
		Builder:      "docker",
		// Patches intentionally nil.
	}
	wire, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(wire), `"patches"`) {
		t.Errorf("nil Patches should be omitted from JSON; got:\n%s", string(wire))
	}
}
