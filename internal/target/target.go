package target

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// Dialer is optionally implemented by targets that can reach their own
// loopback through an existing connection (for example, SSH direct-tcpip).
// DialContext callers must pass an address on the target's own loopback.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// HTTPClient returns an HTTP client that reaches node-local addresses through
// the target when it supports dialing. Local and test targets use the normal
// host transport.
func HTTPClient(t Target, timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if d, ok := t.(Dialer); ok {
		client.Transport = &http.Transport{DialContext: d.DialContext}
	}
	return client
}

// EndpointHost returns the host used when reporting a target endpoint.
func EndpointHost(targetType, host string) string {
	if targetType == "ssh" && host != "" {
		return host
	}
	return "127.0.0.1"
}

// DialContext dials through the target when it implements Dialer (reaching
// the target's own loopback, e.g. over an SSH direct-tcpip channel), and
// falls back to a plain net.Dialer otherwise.
func DialContext(ctx context.Context, t Target, network, addr string, timeout time.Duration) (net.Conn, error) {
	if d, ok := t.(Dialer); ok {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		type dialResult struct {
			conn net.Conn
			err  error
		}
		ch := make(chan dialResult, 1)
		go func() {
			conn, err := d.DialContext(ctx, network, addr)
			ch <- dialResult{conn, err}
		}()
		select {
		case r := <-ch:
			return r.conn, r.err
		case <-ctx.Done():
			// A dial that succeeds after timeout may leave an unowned conn;
			// the SSH/session lifetime will reclaim it. This is an accepted
			// tradeoff for making the timeout preemptible.
			return nil, ctx.Err()
		}
	}
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, addr)
}

// Get issues an HTTP GET through the target-aware client and returns the
// response body. Non-2xx statuses are an error carrying a body snippet.
func Get(ctx context.Context, t Target, url string, timeout time.Duration) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return do(HTTPClient(t, timeout), req)
}

// Post issues an HTTP POST with a JSON body through the target-aware client
// and returns the response body. Non-2xx statuses are an error.
func Post(ctx context.Context, t Target, url string, body []byte, timeout time.Duration) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(HTTPClient(t, timeout), req)
}

func do(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := out
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, snippet)
	}
	return out, nil
}

// Target abstracts command execution and file operations on a deployment target.
type Target interface {
	// Exec runs a command on the target and returns combined output.
	Exec(ctx context.Context, cmd string, args ...string) ([]byte, error)

	// Upload copies a local file to the target.
	Upload(ctx context.Context, localPath, remotePath string) error

	// Download copies a file from the target to local.
	Download(ctx context.Context, remotePath, localPath string) error

	// ReadFile reads a file from the target.
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// WriteFile writes data to a file on the target.
	WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error

	// DiskFree returns available disk space in bytes at the given path.
	DiskFree(ctx context.Context, path string) (uint64, error)

	// MemTotal returns total system memory in bytes.
	MemTotal(ctx context.Context) (uint64, error)

	// PutFile streams a local file to a remote path with atomic
	// install semantics (data lands at <remotePath>.tmp first, then
	// mv renames). Used by the Phase 4 build pipeline to transfer a
	// locally-built JAR to an SSH target. LocalTarget's
	// implementation is a same-fs copy (or no-op when localPath ==
	// remotePath).
	PutFile(ctx context.Context, localPath, remotePath string) error

	// Sha256IfExists returns the hex sha256 of a file at the given
	// path, or empty string if the file does not exist. Used to skip
	// transfer when the target already holds the bit-identical
	// artifact.
	Sha256IfExists(ctx context.Context, path string) (string, error)

	// CommandExists reports whether the named executable resolves
	// on the target's PATH. Used by preflight to fail-fast before
	// apply if a required tool (scp, sha256sum, ...) is missing.
	CommandExists(ctx context.Context, name string) bool

	// String returns a human-readable description of the target.
	String() string
}
