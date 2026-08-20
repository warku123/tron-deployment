package target

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// LocalTargetNoDial satisfies Target WITHOUT the optional Dialer interface,
// to prove the fallback paths (plain host transport / net.Dialer) still
// work for targets that cannot tunnel. It must NOT embed LocalTarget: the
// embedded *LocalTarget method set would promote DialContext onto it and
// silently re-implement Dialer. Methods are never exercised by the tests
// below (they only dial), so they fail loudly if that ever changes.
type LocalTargetNoDial struct{}

type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type blockingTarget struct{ LocalTargetNoDial }

func (blockingTarget) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return blockingDialer{}.DialContext(ctx, network, addr)
}

var _ Target = (*LocalTargetNoDial)(nil)

func (l *LocalTargetNoDial) Exec(context.Context, string, ...string) ([]byte, error) {
	return nil, fmt.Errorf("LocalTargetNoDial: Exec not implemented")
}
func (l *LocalTargetNoDial) Upload(context.Context, string, string) error {
	return fmt.Errorf("LocalTargetNoDial: Upload not implemented")
}
func (l *LocalTargetNoDial) Download(context.Context, string, string) error {
	return fmt.Errorf("LocalTargetNoDial: Download not implemented")
}
func (l *LocalTargetNoDial) ReadFile(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("LocalTargetNoDial: ReadFile not implemented")
}
func (l *LocalTargetNoDial) WriteFile(context.Context, string, []byte, os.FileMode) error {
	return fmt.Errorf("LocalTargetNoDial: WriteFile not implemented")
}
func (l *LocalTargetNoDial) DiskFree(context.Context, string) (uint64, error) {
	return 0, fmt.Errorf("LocalTargetNoDial: DiskFree not implemented")
}
func (l *LocalTargetNoDial) MemTotal(context.Context) (uint64, error) {
	return 0, fmt.Errorf("LocalTargetNoDial: MemTotal not implemented")
}
func (l *LocalTargetNoDial) PutFile(context.Context, string, string) error {
	return fmt.Errorf("LocalTargetNoDial: PutFile not implemented")
}
func (l *LocalTargetNoDial) Sha256IfExists(context.Context, string) (string, error) {
	return "", fmt.Errorf("LocalTargetNoDial: Sha256IfExists not implemented")
}
func (l *LocalTargetNoDial) CommandExists(context.Context, string) bool { return false }
func (l *LocalTargetNoDial) String() string                             { return "local-no-dial" }

// TestEndpointHost pins the ssh-vs-local reporting rule: ssh targets with a
// recorded host report that host; everything else reports loopback.
func TestEndpointHost(t *testing.T) {
	cases := []struct {
		targetType, host, want string
	}{
		{"ssh", "10.0.0.5", "10.0.0.5"},
		{"ssh", "", "127.0.0.1"}, // recorded host missing → fail-safe loopback
		{"local", "", "127.0.0.1"},
		{"local", "ignored", "127.0.0.1"},
		{"", "", "127.0.0.1"},
	}
	for _, tc := range cases {
		if got := EndpointHost(tc.targetType, tc.host); got != tc.want {
			t.Errorf("EndpointHost(%q, %q) = %q, want %q", tc.targetType, tc.host, got, tc.want)
		}
	}
}

// TestLocalTargetDialContext proves the local DialContext reaches a real
// listener — this is what makes target.Get/Post work unchanged for local
// targets after the ssh-tunnel migration.
func TestLocalTargetDialContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	conn, err := NewLocalTarget().DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()
}

// TestHTTPClientFallsBackForNonDialer: a Target that does not implement
// Dialer must get a working default-transport client (plain host dial).
func TestHTTPClientFallsBackForNonDialer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := HTTPClient(&LocalTargetNoDial{}, 2*time.Second)
	if _, ok := client.Transport.(*http.Transport); ok {
		t.Fatalf("non-Dialer target must keep the default transport, got a custom one")
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET via fallback client: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestDialContextFallback: the free DialContext helper falls back to a plain
// net.Dialer when the target does not implement Dialer.
func TestDialContextFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	conn, err := DialContext(context.Background(), &LocalTargetNoDial{}, "tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("DialContext fallback: %v", err)
	}
	conn.Close()
}

func TestSSHTargetDialContextNilClient(t *testing.T) {
	_, err := NewSSHTarget("host", 22, "user", "").DialContext(context.Background(), "tcp", "127.0.0.1:8090")
	if err == nil || !strings.Contains(err.Error(), "ssh not connected") {
		t.Fatalf("DialContext nil client error = %v", err)
	}
}

func TestSSHTargetDialContextRejectsNonLoopback(t *testing.T) {
	_, err := NewSSHTarget("host", 22, "user", "").DialContext(context.Background(), "tcp", "10.0.0.5:8090")
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("DialContext non-loopback error = %v", err)
	}
}

func TestDialContextDialerHonorsTimeout(t *testing.T) {
	start := time.Now()
	_, err := DialContext(context.Background(), &blockingTarget{}, "tcp", "127.0.0.1:8090", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("DialContext timeout error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("DialContext took %s, want near 50ms", elapsed)
	}
}

// TestGetNon2xxIsError pins the status-code contract: a non-2xx response is
// an error carrying a body snippet (curl -fsS semantics the probes replaced).
func TestGetNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	}))
	defer srv.Close()

	_, err := Get(context.Background(), NewLocalTarget(), srv.URL, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Fatalf("Get on 500 = %v, want http 500 error", err)
	}
}

// TestPostSendsJSONBody verifies Post sets the JSON content type and ships
// the body through (getblockbynum-style probes depend on it).
func TestPostSendsJSONBody(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	if _, err := Post(context.Background(), NewLocalTarget(), srv.URL, []byte(`{"num":0}`), 2*time.Second); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if gotBody != `{"num":0}` {
		t.Errorf("body = %q, want %q", gotBody, `{"num":0}`)
	}
}
