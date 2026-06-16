package intent

import "testing"

func TestIsPrivate(t *testing.T) {
	cases := map[string]bool{
		"private": true,
		"mainnet": false,
		"nile":    false,
		"":        false, // unknown → not private (fail-safe)
		"Private": false, // case-sensitive: only the exact "private"
	}
	for network, want := range cases {
		if got := IsPrivate(network); got != want {
			t.Errorf("IsPrivate(%q) = %v; want %v", network, got, want)
		}
	}
}
