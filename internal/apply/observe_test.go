package apply

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/state"
)

// execTarget is a fakeTarget whose Exec is scripted per-test. It reuses
// fakeTarget's no-op implementations for the rest of the interface.
type execTarget struct {
	*fakeTarget
	exec func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (e *execTarget) Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return e.exec(ctx, name, args...)
}

func newExecTarget(fn func(ctx context.Context, name string, args ...string) ([]byte, error)) *execTarget {
	return &execTarget{fakeTarget: &fakeTarget{}, exec: fn}
}

// httpTarget is a fakeTarget whose DialContext redirects loopback probes to
// a test HTTP server. Jar-runtime LiveStatus probes go through the
// target-aware HTTP client (target.Get/Post over DialContext), so the fake
// must serve real HTTP rather than script Exec output.
type httpTarget struct {
	*fakeTarget
	server *httptest.Server
}

func (h *httpTarget) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	addr := strings.TrimPrefix(h.server.URL, "http://")
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// newHTTPStatusTarget starts an httptest server that answers the LiveStatus
// probe endpoints and returns a target wired to it plus the port LiveStatus
// should probe. handler may return ("", true) to fail a request.
func newHTTPStatusTarget(t *testing.T, respond func(path string, body []byte) (string, bool)) (*httpTarget, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		out, fail := respond(r.URL.Path, reqBody)
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, out)
	}))
	t.Cleanup(srv.Close)
	port, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatalf("parse httptest port: %v", err)
	}
	return &httpTarget{fakeTarget: &fakeTarget{}, server: srv}, port
}

func TestLogsDescriptor_Docker(t *testing.T) {
	node := &state.ManagedNode{Name: "fn0", Runtime: "docker"}
	got := LogsDescriptor(node)
	if got["runtime"] != "docker" {
		t.Fatalf("runtime = %v, want docker", got["runtime"])
	}
	if got["container"] != "fn0" {
		t.Errorf("container = %v, want fn0", got["container"])
	}
	if got["path"] != "/java-tron/logs/tron.log" {
		t.Errorf("path = %v, want the in-container tron.log", got["path"])
	}
	if _, ok := got["unit"]; ok {
		t.Errorf("docker descriptor must not carry a systemd unit: %v", got)
	}
}

func TestLogsDescriptor_Jar(t *testing.T) {
	node := &state.ManagedNode{Name: "sr1", Runtime: "jar"}
	got := LogsDescriptor(node)
	if got["runtime"] != "jar" {
		t.Fatalf("runtime = %v, want jar", got["runtime"])
	}
	if got["unit"] != "tron-sr1.service" {
		t.Errorf("unit = %v, want tron-sr1.service", got["unit"])
	}
	if _, ok := got["path"]; ok {
		t.Errorf("jar descriptor must not carry a container path: %v", got)
	}
}

func TestLogsDescriptor_Nil(t *testing.T) {
	if got := LogsDescriptor(nil); got != nil {
		t.Errorf("LogsDescriptor(nil) = %v, want nil", got)
	}
}

func TestContainerID(t *testing.T) {
	const valid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 hex
	node := &state.ManagedNode{Name: "fn0", Runtime: "docker"}

	cases := []struct {
		name    string
		runtime string
		out     string
		err     bool
		want    string
	}{
		{"valid id", "docker", valid + "\n", false, valid},
		{"jar runtime skipped", "jar", valid, false, ""},
		{"docker error", "docker", "", true, ""},
		{"too short", "docker", "abc123", false, ""},
		{"non-hex garbage", "docker", "Error: No such object: fn0", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := *node
			n.Runtime = tc.runtime
			tgt := newExecTarget(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				if tc.err {
					return nil, context.DeadlineExceeded
				}
				return []byte(tc.out), nil
			})
			if got := ContainerID(context.Background(), tgt, &n); got != tc.want {
				t.Errorf("ContainerID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestContainerID_NilTarget(t *testing.T) {
	node := &state.ManagedNode{Name: "fn0", Runtime: "docker"}
	if got := ContainerID(context.Background(), nil, node); got != "" {
		t.Errorf("ContainerID(nil target) = %q, want empty", got)
	}
}

func TestLiveStatus_SetsHealthy(t *testing.T) {
	tgt, port := newHTTPStatusTarget(t, func(path string, _ []byte) (string, bool) {
		switch path {
		case "/wallet/getnowblock":
			return `{"block_header":{"raw_data":{"number":42,"timestamp":1}}}`, false
		case "/wallet/listnodes":
			return `{"nodes":[{},{}]}`, false
		}
		return "", true
	})
	node := &state.ManagedNode{Name: "fn0", Runtime: "jar", HTTPPort: port}
	out := LiveStatus(context.Background(), tgt, node)
	if out["healthy"] != true {
		t.Errorf("healthy = %v, want true (RPC answered with a parseable block)", out["healthy"])
	}
	if out["block_height"] != int64(42) {
		t.Errorf("block_height = %v, want 42", out["block_height"])
	}
	if out["peer_count"] != 2 {
		t.Errorf("peer_count = %v, want 2", out["peer_count"])
	}
}

const genesisBlockID = "0000000000000000957dc2d350daecc7bb6a38f3938ebde0a0c1cedafe15f0ed" // 64 hex, block-0 shape

func TestIsHex64(t *testing.T) {
	if !isHex64(genesisBlockID) {
		t.Errorf("isHex64(%q) = false, want true", genesisBlockID)
	}
	for _, bad := range []string{"", "abc", strings.ToUpper(genesisBlockID), genesisBlockID + "0", "g" + genesisBlockID[1:]} {
		if isHex64(bad) {
			t.Errorf("isHex64(%q) = true, want false", bad)
		}
	}
}

// TestLiveStatus_GenesisBlockID covers the block-0 probe: a valid blockID
// becomes genesis_block_id; a malformed or failed probe omits it (it's
// optional metadata, never starving the primary signals).
func TestLiveStatus_GenesisBlockID(t *testing.T) {
	blockJSON := `{"block_header":{"raw_data":{"number":3,"timestamp":1}}}`

	mk := func(genesisBody string, genesisErr bool) (*httpTarget, int) {
		return newHTTPStatusTarget(t, func(path string, _ []byte) (string, bool) {
			switch path {
			case "/wallet/getblockbynum":
				if genesisErr {
					return "", true
				}
				return genesisBody, false
			case "/wallet/getnowblock":
				return blockJSON, false
			}
			return "", true
		})
	}

	// Valid blockID → surfaced.
	tgt, port := newHTTPStatusTarget(t, func(path string, _ []byte) (string, bool) {
		if path == "/wallet/getblockbynum" {
			return `{"blockID":"` + genesisBlockID + `","block_header":{}}`, false
		}
		if path == "/wallet/getnowblock" {
			return blockJSON, false
		}
		return "", true
	})
	out := LiveStatus(context.Background(), tgt, &state.ManagedNode{Name: "fn0", Runtime: "jar", HTTPPort: port})
	if out["genesis_block_id"] != genesisBlockID {
		t.Errorf("genesis_block_id = %v, want %s", out["genesis_block_id"], genesisBlockID)
	}

	// Malformed blockID (not 64-hex) → absent.
	tgt, port = mk(`{"blockID":"nope"}`, false)
	out = LiveStatus(context.Background(), tgt, &state.ManagedNode{Name: "fn0", Runtime: "jar", HTTPPort: port})
	if _, ok := out["genesis_block_id"]; ok {
		t.Errorf("genesis_block_id must be absent for a malformed blockID; got %v", out["genesis_block_id"])
	}

	// Probe failed (node down) → absent, but the primary healthy signal
	// (from getnowblock) is unaffected — proves it doesn't starve them.
	tgt, port = mk("", true)
	out = LiveStatus(context.Background(), tgt, &state.ManagedNode{Name: "fn0", Runtime: "jar", HTTPPort: port})
	if _, ok := out["genesis_block_id"]; ok {
		t.Errorf("genesis_block_id must be absent when the probe fails; got %v", out["genesis_block_id"])
	}
	if out["healthy"] != true {
		t.Errorf("a failed genesis probe must not break healthy; got %v", out["healthy"])
	}
}

func TestLiveStatus_NoHealthyOnProbeFailure(t *testing.T) {
	// No listener behind the target: every dial fails, so every probe errs
	// (a node that is down behaves the same over the tunnel).
	node := &state.ManagedNode{Name: "fn0", Runtime: "jar", HTTPPort: 1}
	out := LiveStatus(context.Background(), &fakeTarget{}, node)
	if _, ok := out["healthy"]; ok {
		t.Errorf("healthy must be absent when the probe fails (caller seeds false); got %v", out["healthy"])
	}
}

// TestLiveStatus_NoHealthyOnEmptyOrErrorBody pins the integrity gate: a 200
// that parses as JSON but isn't a real block (empty `{}`, or an error-shaped
// object — both yield block timestamp 0) must NOT read as healthy, and must
// not fabricate a block_height of 0.
func TestLiveStatus_NoHealthyOnEmptyOrErrorBody(t *testing.T) {
	for _, body := range []string{`{}`, `{"Error":"some node error"}`, `{"block_header":{"raw_data":{"number":0,"timestamp":0}}}`} {
		tgt, port := newHTTPStatusTarget(t, func(path string, _ []byte) (string, bool) {
			if path == "/wallet/getnowblock" {
				return body, false
			}
			return "", true
		})
		node := &state.ManagedNode{Name: "fn0", Runtime: "jar", HTTPPort: port}
		out := LiveStatus(context.Background(), tgt, node)
		if _, ok := out["healthy"]; ok {
			t.Errorf("body %q: healthy must be absent (no real block); got %v", body, out["healthy"])
		}
		if _, ok := out["block_height"]; ok {
			t.Errorf("body %q: block_height must be absent for a non-block response; got %v", body, out["block_height"])
		}
	}
}

// TestLiveStatus_DockerProbeShape covers the docker probe command path
// (`docker exec <name> curl ...`), distinct from the jar host-side curl, so
// a regression in the docker arg construction is caught.
func TestLiveStatus_DockerProbeShape(t *testing.T) {
	node := &state.ManagedNode{Name: "fn0", Runtime: "docker", HTTPPort: 8090}
	var sawDockerExec bool
	tgt := newExecTarget(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" && len(args) >= 2 && args[0] == "exec" && args[1] == "fn0" {
			sawDockerExec = true
		}
		if strings.Contains(args[len(args)-1], "/wallet/getnowblock") {
			return []byte(`{"block_header":{"raw_data":{"number":7,"timestamp":1}}}`), nil
		}
		return nil, context.DeadlineExceeded
	})
	out := LiveStatus(context.Background(), tgt, node)
	if !sawDockerExec {
		t.Errorf("docker node probe must run via `docker exec <name> ...`")
	}
	if out["healthy"] != true {
		t.Errorf("healthy = %v, want true", out["healthy"])
	}
}

func TestLogsDescriptor_UnknownRuntime(t *testing.T) {
	// A node with an unrecorded/future runtime must NOT be mislabeled docker.
	got := LogsDescriptor(&state.ManagedNode{Name: "x", Runtime: ""})
	if got["runtime"] != "" {
		t.Errorf("runtime = %v, want empty (honest unknown)", got["runtime"])
	}
	if _, ok := got["path"]; ok {
		t.Errorf("unknown runtime must not assert a docker path: %v", got)
	}
	if _, ok := got["container"]; ok {
		t.Errorf("unknown runtime must not assert a container: %v", got)
	}
}

func TestNormalizeContainerID(t *testing.T) {
	const valid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := map[string]string{
		valid + "\n":                 valid, // trims whitespace
		"  " + valid + "  ":          valid,
		"abc123":                     "", // too short
		"Error: No such object: fn0": "", // docker error text
		strings.ToUpper(valid):       "", // uppercase hex rejected
		"":                           "",
	}
	for in, want := range cases {
		if got := NormalizeContainerID(in); got != want {
			t.Errorf("NormalizeContainerID(%q) = %q, want %q", in, got, want)
		}
	}
}
