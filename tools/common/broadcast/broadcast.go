// Package broadcast is a tiny HTTP client for java-tron's
// /wallet/broadcasttransaction endpoint. It is shared between
// tools/replay (which forwards mainnet txs) and tools/txgen (which
// broadcasts locally-generated synthetic txs).
//
// Single responsibility: take a serialized transaction (JSON) and
// hand it to a TRON HTTP node, return a (success, message) tuple
// with java-tron's hex-encoded error strings already decoded.
package broadcast

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultTimeoutSec = 5

// Client posts transactions to a TRON HTTP node.
type Client struct {
	baseURL string
	http    *http.Client
}

// resp is the response shape of /wallet/broadcasttransaction.
type resp struct {
	Result  bool   `json:"result"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// New returns a Client targeting baseURL (e.g. "http://10.0.0.1:8090").
// A trailing slash is tolerated.
func New(baseURL string) *Client {
	return NewWithTimeout(baseURL, defaultTimeoutSec*time.Second)
}

// NewWithTimeout is like New but lets callers override the per-request
// timeout. Useful when broadcasting to a slow or distant node.
func NewWithTimeout(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// Broadcast forwards a transaction JSON to the node as-is, without
// rewriting any fields.
//
// Returns (success, message). success=true means the node accepted the
// transaction into its pending pool. Confirming it actually landed in
// a block requires a separate /wallet/gettransactioninfobyid call.
func (c *Client) Broadcast(ctx context.Context, tx json.RawMessage) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/wallet/broadcasttransaction", bytes.NewReader(tx))
	if err != nil {
		return false, "BUILD_REQ_ERROR: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := c.http.Do(req)
	if err != nil {
		return false, "NETWORK_ERROR: " + err.Error()
	}
	defer httpResp.Body.Close()
	body, _ := io.ReadAll(httpResp.Body)
	var r resp
	if err := json.Unmarshal(body, &r); err != nil {
		return false, "PARSE_ERROR: " + string(body)
	}
	if r.Result {
		return true, "OK"
	}
	msg := r.Message
	// java-tron's message field is hex-encoded; decode for readability.
	if msg != "" {
		if decoded, err := hex.DecodeString(msg); err == nil {
			msg = string(decoded)
		}
	}
	return false, r.Code + ": " + msg
}

// BaseURL returns the node base URL the client targets.
func (c *Client) BaseURL() string { return c.baseURL }
