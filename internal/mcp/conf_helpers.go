package mcp

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// readLiveConfigForMCP returns the bytes of the conf file currently
// in use by the running node, regardless of runtime. Shared by
// resources.go (trond://nodes/<name>/conf) and the future
// verify_config tool.
func readLiveConfigForMCP(ctx context.Context, tgt target.Target, node *state.ManagedNode) (string, error) {
	if node.Runtime == "jar" {
		out, err := tgt.Exec(ctx, "cat", filepath.Join(node.InstallPath, "conf", node.Name+".conf"))
		if err != nil {
			return "", fmt.Errorf("read jar conf: %w", err)
		}
		return string(out), nil
	}
	out, err := tgt.Exec(ctx, "docker", "exec", node.Name, "cat",
		render.ContainerConfDir+"/"+node.Name+".conf")
	if err != nil {
		return "", fmt.Errorf("docker exec cat: %w", err)
	}
	return string(out), nil
}
