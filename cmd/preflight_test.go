package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// fakeTarget is a minimal target.Target stub that returns canned disk
// and memory values. Only DiskFree / MemTotal are exercised by the
// preflight check tests; the rest satisfy the interface.
type fakeTarget struct {
	diskFreeBytes uint64
	memTotalBytes uint64
}

func (f *fakeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	return nil, nil
}
func (f *fakeTarget) Upload(ctx context.Context, l, r string) error                          { return nil }
func (f *fakeTarget) Download(ctx context.Context, r, l string) error                        { return nil }
func (f *fakeTarget) ReadFile(ctx context.Context, p string) ([]byte, error)                 { return nil, nil }
func (f *fakeTarget) WriteFile(ctx context.Context, p string, d []byte, m os.FileMode) error { return nil }
func (f *fakeTarget) DiskFree(ctx context.Context, p string) (uint64, error) {
	return f.diskFreeBytes, nil
}
func (f *fakeTarget) MemTotal(ctx context.Context) (uint64, error) { return f.memTotalBytes, nil }
func (f *fakeTarget) String() string                               { return "fake" }

// TestCheckDiskSpace_NetworkThresholds pins the per-network disk
// requirement: only mainnet (the implicit default) demands 100 GB; all
// short-lived test chains relax to 10 GB. Adding a new private-shape
// network must update this list AND extend the allowlist in
// checkDiskSpace, otherwise the new network silently inherits the
// mainnet threshold (the bug we hit on warku123/java-tron PR#4 — the
// CI runner had 87 GB free, which fails mainnet's 100 GB but easily
// clears 10 GB).
func TestCheckDiskSpace_NetworkThresholds(t *testing.T) {
	// 87 GB free — close to what a fresh GitHub Actions
	// ubuntu-latest runner reports.
	const bytes87GB = uint64(87) * 1024 * 1024 * 1024

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	cases := []struct {
		network    string
		wantStatus string
		wantMsg    string // substring match
	}{
		{"mainnet", "fail", "100GB recommended"},
		{"nile", "pass", "87GB free"},
		{"private", "pass", "87GB free"},
		{"system-test", "pass", "87GB free"},
	}

	for _, tc := range cases {
		t.Run(tc.network, func(t *testing.T) {
			tgt := &fakeTarget{
				diskFreeBytes: bytes87GB,
				memTotalBytes: 16 * 1024 * 1024 * 1024,
			}
			i := &intent.Intent{Network: tc.network}
			r := checkDiskSpace(cmd, tgt, i)
			if r.Status != tc.wantStatus {
				t.Errorf("network=%s, got status=%q msg=%q, want status=%q",
					tc.network, r.Status, r.Message, tc.wantStatus)
			}
			if !strings.Contains(r.Message, tc.wantMsg) {
				t.Errorf("network=%s, msg=%q does not contain %q",
					tc.network, r.Message, tc.wantMsg)
			}
		})
	}
}
