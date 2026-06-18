package snapshot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

func seedNode(t *testing.T, n state.ManagedNode) {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(&state.DeploymentState{Version: 1, Nodes: []state.ManagedNode{n}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func wantValErr(t *testing.T, err error, mustContain string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.ExitCode != output.ExitValidationError && se.Code != "NODE_NOT_FOUND" {
		t.Errorf("unexpected error envelope: code=%q exit=%d", se.Code, se.ExitCode)
	}
	if mustContain != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(mustContain)) {
		t.Errorf("error %q should mention %q", err.Error(), mustContain)
	}
}

func TestResolveNodeDBDir_Jar(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "sr0", Status: "stopped", Runtime: "jar", InstallPath: "/srv/tron/sr0",
		Target: state.NodeTarget{Type: "local"}, LastApplied: time.Now().UTC(),
	})
	dir, err := resolveNodeDBDir(context.Background(), "sr0")
	if err != nil {
		t.Fatalf("resolveNodeDBDir: %v", err)
	}
	if dir != filepath.Join("/srv/tron/sr0", "output-directory") {
		t.Errorf("dir = %q; want /srv/tron/sr0/output-directory", dir)
	}
}

func TestResolveNodeDBDir_NotFound(t *testing.T) {
	seedNode(t, state.ManagedNode{Name: "sr0", Status: "stopped", Runtime: "jar"})
	_, err := resolveNodeDBDir(context.Background(), "ghost")
	wantValErr(t, err, "not found")
}

func TestResolveNodeDBDir_RefusesSSH(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "jar", InstallPath: "/x",
		Target: state.NodeTarget{Type: "ssh", Host: "h"}, LastApplied: time.Now().UTC(),
	})
	_, err := resolveNodeDBDir(context.Background(), "fn0")
	wantValErr(t, err, "local-target")
}

func TestResolveNodeDBDir_RefusesRunning(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "fn0", Status: "running", Runtime: "jar", InstallPath: "/x",
		Target: state.NodeTarget{Type: "local"}, LastApplied: time.Now().UTC(),
	})
	_, err := resolveNodeDBDir(context.Background(), "fn0")
	wantValErr(t, err, "stopped")
}

// docker cases swap the dockerInspect seam for canned output.
func withDockerInspect(t *testing.T, fn func(context.Context, string) ([]byte, error)) {
	t.Helper()
	old := dockerInspect
	dockerInspect = fn
	t.Cleanup(func() { dockerInspect = old })
}

func TestResolveNodeDBDir_DockerBind(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "docker",
		Target: state.NodeTarget{Type: "local"}, LastApplied: time.Now().UTC(),
	})
	withDockerInspect(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(`[{"State":{"Running":false},"Mounts":[
			{"Type":"bind","Source":"/host/rig/fn0/output-directory","Destination":"/java-tron/output-directory"}]}]`), nil
	})
	dir, err := resolveNodeDBDir(context.Background(), "fn0")
	if err != nil {
		t.Fatalf("resolveNodeDBDir: %v", err)
	}
	if dir != "/host/rig/fn0/output-directory" {
		t.Errorf("dir = %q; want the bind Source verbatim", dir)
	}
}

func TestResolveNodeDBDir_DockerNamedVolumeRefused(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "docker",
		Target: state.NodeTarget{Type: "local"}, LastApplied: time.Now().UTC(),
	})
	withDockerInspect(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(`[{"State":{"Running":false},"Mounts":[
			{"Type":"volume","Source":"/var/lib/docker/volumes/fn0-data/_data","Destination":"/java-tron/output-directory"}]}]`), nil
	})
	_, err := resolveNodeDBDir(context.Background(), "fn0")
	wantValErr(t, err, "volume")
}

func TestResolveNodeDBDir_DockerStillRunningRefused(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "docker", // state stale; live says running
		Target: state.NodeTarget{Type: "local"}, LastApplied: time.Now().UTC(),
	})
	withDockerInspect(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(`[{"State":{"Running":true},"Mounts":[
			{"Type":"bind","Source":"/host/fn0","Destination":"/java-tron/output-directory"}]}]`), nil
	})
	_, err := resolveNodeDBDir(context.Background(), "fn0")
	wantValErr(t, err, "running")
}

func TestResolveNodeDBDir_DockerRemovedRefused(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "docker",
		Target: state.NodeTarget{Type: "local"}, LastApplied: time.Now().UTC(),
	})
	withDockerInspect(t, func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("Error: No such object: fn0")
	})
	_, err := resolveNodeDBDir(context.Background(), "fn0")
	wantValErr(t, err, "removed")
}
