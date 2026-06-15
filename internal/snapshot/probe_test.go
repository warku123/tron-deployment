package snapshot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// buildProbeMirror returns a Source wired to a fresh httptest server
// that serves a LiteFullNode tarball at exactly the backup dates
// passed in. A HEAD to any other backup path returns 404. The server
// itself is cleaned up via t.Cleanup — callers never need to touch
// it, which is why only Source is returned (unparam-clean).
func buildProbeMirror(t *testing.T, serveDates ...string) Source {
	t.Helper()
	mux := http.NewServeMux()
	for _, d := range serveDates {
		path := fmt.Sprintf("/backup%s/LiteFullNode_output-directory.tgz", d)
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusOK)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Source{
		Network:       NetworkNile,
		DBKind:        DBKindLite,
		DBEngine:      EngineLevelDB,
		Domain:        "test.local",
		BaseURL:       srv.URL,
		IndexStrategy: "date",
	}
}

func TestProbe_OKWhenRecentBackupServes(t *testing.T) {
	// Yesterday is always in the generated date list (i starts at 1).
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("20060102")
	src := buildProbeMirror(t, yesterday)

	r := Probe(context.Background(), src, ProbeOptions{
		HTTPTimeout: 2 * time.Second,
	})
	if r.Status != ProbeOK {
		t.Fatalf("status: want %s, got %s (err=%s)", ProbeOK, r.Status, r.Err)
	}
	if r.LatestBackup != "backup"+yesterday {
		t.Fatalf("LatestBackup: want backup%s, got %s", yesterday, r.LatestBackup)
	}
	if r.LatestAgeDays != 1 {
		t.Fatalf("LatestAgeDays: want 1, got %d", r.LatestAgeDays)
	}
}

func TestProbe_StaleWhenOnlyOldBackupServes(t *testing.T) {
	// Serve a backup that is older than the 7-day staleness threshold but
	// still inside generateDateList's DENSE tier — every day within the
	// first 30 days back is emitted, so a date 15 days back is always a
	// probe candidate regardless of which calendar day the test runs on,
	// and 15 > 7 makes it reliably stale.
	//
	// The previous logic ("45 days back, snapped backward to the nearest
	// 10/20/30 day-of-month") was date-dependent and flaked: when
	// today-45 landed on a day-of-month < 10 the backward snap found no
	// 10/20/30 day, left the served date on a day generateDateList never
	// emits, and the probe returned Unreachable instead of Stale.
	old := time.Now().UTC().AddDate(0, 0, -15)
	oldStr := old.Format("20060102")
	src := buildProbeMirror(t, oldStr)

	r := Probe(context.Background(), src, ProbeOptions{
		HTTPTimeout:   2 * time.Second,
		StaleAfter:    7 * 24 * time.Hour,
		MaxCandidates: 100, // walk far enough back to hit the old date
	})
	if r.Status != ProbeStale {
		t.Fatalf("status: want %s, got %s (err=%s, latest=%s)",
			ProbeStale, r.Status, r.Err, r.LatestBackup)
	}
	if r.LatestAgeDays < 7 {
		t.Fatalf("expected age > threshold, got %d days", r.LatestAgeDays)
	}
}

func TestProbe_UnreachableWhenNothingServes(t *testing.T) {
	src := buildProbeMirror(t /* no served dates */)
	r := Probe(context.Background(), src, ProbeOptions{
		HTTPTimeout:   1 * time.Second,
		MaxCandidates: 5,
	})
	if r.Status != ProbeUnreachable {
		t.Fatalf("status: want %s, got %s", ProbeUnreachable, r.Status)
	}
}

func TestProbe_BadConfigOnUnknownStrategy(t *testing.T) {
	src := Source{IndexStrategy: "wat", BaseURL: "http://localhost"}
	r := Probe(context.Background(), src, ProbeOptions{})
	if r.Status != ProbeBadConfig {
		t.Fatalf("status: want %s, got %s", ProbeBadConfig, r.Status)
	}
}

func TestBackupAgeDays(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name   string
		input  string
		wantOK bool
	}{
		{"unhyphenated 5d", "backup" + now.AddDate(0, 0, -5).Format("20060102"), true},
		{"hyphenated 3d", "backup" + now.AddDate(0, 0, -3).Format("2006-01-02"), true},
		{"unparseable", "backupNOPE", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := backupAgeDays(c.input)
			if c.wantOK && got < 0 {
				t.Errorf("input %q: expected a non-negative age, got %d", c.input, got)
			}
			if !c.wantOK && got >= 0 {
				t.Errorf("input %q: expected -1 sentinel, got %d", c.input, got)
			}
		})
	}
}

func TestProbeAll_PreservesInputOrder(t *testing.T) {
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("20060102")
	srcA := buildProbeMirror(t, yesterday)
	srcA.Domain = "alpha"
	srcB := buildProbeMirror(t /* nothing */)
	srcB.Domain = "bravo"
	results := ProbeAll(context.Background(), []Source{srcA, srcB}, ProbeOptions{
		HTTPTimeout:   1 * time.Second,
		MaxCandidates: 3,
	}, 2)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if results[0].Source.Domain != "alpha" || results[1].Source.Domain != "bravo" {
		t.Fatalf("order not preserved: %s, %s",
			results[0].Source.Domain, results[1].Source.Domain)
	}
	if results[0].Status != ProbeOK {
		t.Errorf("alpha: want ok, got %s", results[0].Status)
	}
	if results[1].Status != ProbeUnreachable {
		t.Errorf("bravo: want unreachable, got %s", results[1].Status)
	}
}
