package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSwapComposeImageTag(t *testing.T) {
	tests := []struct {
		name, compose, version, want, old, image string
		wantErr                                  bool
	}{
		{"official", "image: tronprotocol/java-tron:4.8.0\n", "4.8.1", "image: tronprotocol/java-tron:4.8.1\n", "tronprotocol/java-tron:4.8.0", "tronprotocol/java-tron:4.8.1", false},
		{"registry port", "image: registry:5000/img:1.0\n", "2.0", "image: registry:5000/img:2.0\n", "registry:5000/img:1.0", "registry:5000/img:2.0", false},
		{"digest pin", "image: img:1.0@sha256:abc\n", "2.0", "image: img:2.0\n", "img:1.0@sha256:abc", "img:2.0", false},
		{"same version", "image: img:1.0\n", "1.0", "image: img:1.0\n", "img:1.0", "img:1.0", false},
		{"missing", "services:\n  node:\n    command: tron\n", "1.0", "", "", "", true},
		{"different images", "image: a:1\nother:\n  image: b:1\n", "2", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, old, image, err := swapComposeImageTag([]byte(tc.compose), tc.version)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if string(got) != tc.want || old != tc.old || image != tc.image {
				t.Fatalf("got %q, %q, %q", got, old, image)
			}
		})
	}
}

type upgradeFakeTarget struct {
	*fakeTarget
	pullErr error
}

type jarUpgradeFakeTarget struct{ *fakeTarget }

type failingUpgradeTarget struct {
	*fakeTarget
	writeErr      error
	startErr      error
	startFailures int
	mvFailAt      int
	mvCount       int
}

func (f *failingUpgradeTarget) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	return f.fakeTarget.WriteFile(ctx, path, data, perm)
}

func (f *failingUpgradeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	if cmd == "docker" && len(args) > 0 && args[len(args)-1] == "--force-recreate" {
		if f.startFailures > 0 {
			f.startFailures--
			return nil, f.startErr
		}
	}
	if cmd == "mv" {
		f.mvCount++
		if f.mvFailAt == f.mvCount {
			return nil, fmt.Errorf("mv %s to %s failed", args[0], args[1])
		}
	}
	return nil, nil
}

func (f *jarUpgradeFakeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	if cmd == "sha256sum" {
		return []byte("deadbeef  /opt/tron/FullNode.jar\n"), nil
	}
	return nil, nil
}

func (f *upgradeFakeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	if cmd == "docker" && len(args) >= 2 && args[0] == "pull" {
		return nil, f.pullErr
	}
	return nil, nil
}

func TestDockerRuntimeUpgradeArtifact(t *testing.T) {
	path := "/deploy/n1/docker-compose.yaml"
	f := &upgradeFakeTarget{fakeTarget: newFakeTarget()}
	f.files[path] = []byte("services:\n  n1:\n    image: img:1\n")
	tx, err := NewDockerRuntime(f, "/deploy").PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := string(f.files[path]); !strings.Contains(got, "img:2") {
		t.Fatalf("compose not updated: %s", got)
	}
	if !reflect.DeepEqual(f.cmds[0], []string{"docker", "pull", "img:2"}) {
		t.Fatalf("commands = %v", f.cmds)
	}

	f = &upgradeFakeTarget{fakeTarget: newFakeTarget(), pullErr: errors.New("no image")}
	f.files[path] = []byte("image: img:1\n")
	if _, err := NewDockerRuntime(f, "/deploy").PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"}); err == nil {
		t.Fatal("expected pull error")
	}
	if string(f.files[path]) != "image: img:1\n" {
		t.Fatal("compose changed after failed pull")
	}
}

func TestDockerArtifactActivateWriteFailureDoesNotStart(t *testing.T) {
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget(), writeErr: errors.New("write failed")}
	tx := &dockerArtifactTransaction{r: NewDockerRuntime(f, "/deploy"), name: "n1", path: "/deploy/n1/docker-compose.yaml", updated: []byte("new")}
	if err := tx.Activate(context.Background()); err == nil {
		t.Fatal("Activate succeeded despite compose write failure")
	}
	for _, cmd := range f.cmds {
		if len(cmd) > 0 && cmd[0] == "docker" {
			t.Fatal("docker Start command was invoked after Activate failure")
		}
	}
}

func TestDockerArtifactStartFailureRollbackRestoresCompose(t *testing.T) {
	old := []byte("image: img:1\n")
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget(), startErr: errors.New("up failed"), startFailures: 1}
	path := "/deploy/n1/docker-compose.yaml"
	tx := &dockerArtifactTransaction{r: NewDockerRuntime(f, "/deploy"), name: "n1", path: path, old: old, updated: []byte("image: img:2\n")}
	if err := tx.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded unexpectedly")
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.files[path], old) {
		t.Fatalf("compose = %q, want old compose %q", f.files[path], old)
	}
	if got := countCommand(f.cmds, "docker"); got != 2 {
		t.Fatalf("docker compose calls = %d, want failed up plus rollback up", got)
	}
}

func TestJarArtifactActivateSecondMoveSelfRestoresBackup(t *testing.T) {
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget(), mvFailAt: 2}
	tx := &jarArtifactTransaction{r: NewJarRuntime(f), candidate: "/opt/tron/FullNode.jar.candidate", live: "/opt/tron/FullNode.jar", backup: "/opt/tron/FullNode.jar.upgrade.backup"}
	if err := tx.Activate(context.Background()); err == nil {
		t.Fatal("Activate succeeded despite candidate move failure")
	} else if !strings.Contains(err.Error(), "/opt/tron/FullNode.jar.candidate") {
		t.Fatalf("error lacks artifact paths: %v", err)
	}
	if got := f.cmds[len(f.cmds)-1]; !reflect.DeepEqual(got, []string{"mv", "/opt/tron/FullNode.jar.upgrade.backup", "/opt/tron/FullNode.jar"}) {
		t.Fatalf("last recovery command = %v", got)
	}
}

func TestJarArtifactRollbackSequence(t *testing.T) {
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget()}
	tx := &jarArtifactTransaction{r: NewJarRuntime(f), name: "n1", candidate: "/opt/tron/FullNode.jar.candidate", live: "/opt/tron/FullNode.jar", backup: "/opt/tron/FullNode.jar.upgrade.backup", activated: true}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"mv", tx.live, tx.candidate}, {"mv", tx.backup, tx.live}, {"systemctl", "start", "tron-n1.service"}}
	if !reflect.DeepEqual(f.cmds, want) {
		t.Fatalf("rollback commands = %v, want %v", f.cmds, want)
	}
}

func TestValidateUpgradeVersionRejectsUnsafeTags(t *testing.T) {
	for _, version := range []string{"../../etc", "tag with spaces"} {
		if err := validateUpgradeVersion(version); err == nil {
			t.Errorf("validateUpgradeVersion(%q) accepted unsafe tag", version)
		}
	}
}

func countCommand(cmds [][]string, name string) int {
	count := 0
	for _, cmd := range cmds {
		if len(cmd) > 0 && cmd[0] == name {
			count++
		}
	}
	return count
}

func TestJarRuntimeUpgradeArtifact(t *testing.T) {
	t.Run("missing URL", func(t *testing.T) {
		r := NewJarRuntime(newFakeTarget())
		r.SetPurgeInstallPath("/opt/tron")
		if _, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"}); err == nil || !strings.Contains(err.Error(), "jar URL") {
			t.Fatal(err)
		}
	})
	t.Run("download and restart", func(t *testing.T) {
		f := &jarUpgradeFakeTarget{newFakeTarget()}
		r := NewJarRuntime(f)
		r.SetPurgeInstallPath("/opt/tron")
		tx, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2", JarURL: "https://example/jar"})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Activate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.cmds[0][0] != "curl" || f.cmds[len(f.cmds)-1][0] != "mv" {
			t.Fatalf("commands = %v", f.cmds)
		}
	})
	t.Run("verified existing jar", func(t *testing.T) {
		f := &jarUpgradeFakeTarget{newFakeTarget()}
		f.files["/opt/tron/FullNode.jar"] = []byte("old")
		r := NewJarRuntime(f)
		r.SetPurgeInstallPath("/opt/tron")
		tx, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2", JarURL: "https://example/jar", JarSHA256: "deadbeef"})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Activate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.cmds[0][0] != "curl" {
			t.Fatalf("commands = %v", f.cmds)
		}
	})
}

func TestJarDeployInterruptedArtifactConvergesOnRerun(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded string
		restart  bool
	}{
		{name: "missing recorded digest", recorded: "", restart: true},
		{name: "matching recorded digest", recorded: "deadbeef", restart: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &jarUpgradeFakeTarget{newFakeTarget()}
			f.files["/opt/tron/FullNode.jar"] = []byte("new artifact")
			r := NewJarRuntime(f)
			err := r.Deploy(context.Background(), DeployOpts{
				Name:           "n1",
				JarPath:        "/opt/tron/FullNode.jar",
				JarURL:         "https://example/jar",
				JarSHA256:      "deadbeef",
				ArtifactSHA256: tc.recorded,
				ConfigData:     []byte("config"),
				SystemdData:    []byte("unit"),
			})
			if err != nil {
				t.Fatal(err)
			}
			gotRestart := false
			for _, cmd := range f.cmds {
				if len(cmd) >= 2 && cmd[0] == "systemctl" && cmd[1] == "restart" {
					gotRestart = true
				}
			}
			if gotRestart != tc.restart {
				t.Fatalf("restart = %v, want %v; commands = %v", gotRestart, tc.restart, f.cmds)
			}
		})
	}
}
