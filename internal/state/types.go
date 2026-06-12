package state

import "time"

// ManagedNode represents a deployed node tracked in the state file.
type ManagedNode struct {
	Name            string     `json:"name"`
	IntentHash      string     `json:"intent_hash"`
	ConfigHash      string     `json:"config_hash"`
	Version         string     `json:"version"`
	Target          NodeTarget `json:"target"`
	Runtime         string     `json:"runtime"`
	Status          string     `json:"status"` // running, stopped, error, unknown
	LastApplied     time.Time  `json:"last_applied"`
	PreviousVersion string     `json:"previous_version,omitempty"`
	ComposePath     string     `json:"compose_path,omitempty"`
	SystemdUnit     string     `json:"systemd_unit,omitempty"`
	InstallPath     string     `json:"install_path,omitempty"`
	// HTTPPort and GRPCPort capture the API ports as configured at deploy time
	// so probe commands (health, diagnose, verify) can target the right port
	// without re-reading the intent file. Older state files predate these
	// fields — callers must fall back to defaults when zero.
	HTTPPort int `json:"http_port,omitempty"`
	GRPCPort int `json:"grpc_port,omitempty"`
	// P2PPort is the listen.port a sibling can dial to peer with this node.
	// `network add` reads it from every existing entry to populate the new
	// node's active_peers so it can immediately join the P2P mesh.
	P2PPort int `json:"p2p_port,omitempty"`
	// Labels mirror intent.NodeSpec.Labels and survive across CLI sessions
	// so test harnesses can filter via `trond list --label key=value`
	// without touching the original intent file.
	Labels map[string]string `json:"labels,omitempty"`

	// BuildCacheKey records the content-addressed build that produced
	// the artifact currently deployed for this node. Empty when the
	// node consumed a pre-built image or jar source. `trond build
	// prune` (FR-018) cross-references this field and refuses to
	// delete an artifact a running node depends on. Per spec/002 the
	// stored value is the full cache key (`<sha>-b<digest>[+dirty-...]`),
	// not just a git revision.
	BuildCacheKey string `json:"build_cache_key,omitempty"`
}

// NodeTarget is the target info stored in state (subset of intent.Target).
type NodeTarget struct {
	Type         string `json:"type"`
	Host         string `json:"host,omitempty"`
	User         string `json:"user,omitempty"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

// DeploymentState is the top-level state file structure.
type DeploymentState struct {
	Version int           `json:"version"`
	Nodes   []ManagedNode `json:"nodes"`
}

// AuditEntry represents a single line in the audit log (JSONL format).
type AuditEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Command    string    `json:"command"`
	Node       string    `json:"node,omitempty"`
	Target     string    `json:"target"`
	IntentHash string    `json:"intent_hash,omitempty"`
	Result     string    `json:"result"` // success, failure, no_change
	DurationMs int64     `json:"duration_ms"`
	ErrorCode  string    `json:"error_code,omitempty"`
}
