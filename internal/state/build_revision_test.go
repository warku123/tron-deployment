package state

import "testing"

func TestManagedNode_BuildRevision(t *testing.T) {
	cases := []struct {
		name     string
		cacheKey string
		want     string
	}{
		{"clean build", "abc123def456-b1234abcd", "abc123def456"},
		{"dirty build", "abc123def456-b1234abcd+dirty-deadbeef", "abc123def456"},
		{"patched build", "0123456789ab-bcafef0012-xdeadbeef", "0123456789ab"},
		{"no build (pre-built image/jar)", "", ""},
		{"non-hex prefix rejected", "nothex123456-b1234abcd", ""},
		{"short prefix rejected", "abc-b1234abcd", ""},
		{"uppercase rejected", "ABC123DEF456-b1234abcd", ""},
		{"prefix-only, no -b", "abc123def456", "abc123def456"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := ManagedNode{BuildCacheKey: tc.cacheKey}
			if got := n.BuildRevision(); got != tc.want {
				t.Errorf("BuildRevision(%q) = %q, want %q", tc.cacheKey, got, tc.want)
			}
		})
	}
}
