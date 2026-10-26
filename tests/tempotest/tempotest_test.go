package tempotest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSpec(t *testing.T) {
	t.Setenv("TEMPO_LOCALNET_IMAGE", "")
	composeFile, err := ComposeFile()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ReadSpec(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(spec.Image, "ghcr.io/tempoxyz/tempo-localnet@sha256:") {
		t.Errorf("image = %q, want a digest-pinned ghcr.io/tempoxyz/tempo-localnet", spec.Image)
	}
	if strings.Join(spec.Command, " ") != "--block-time 1ms" {
		t.Errorf("command = %q, want [--block-time 1ms]", spec.Command)
	}

	t.Setenv("TEMPO_LOCALNET_IMAGE", "example.com/tempo:dev")
	spec, err = ReadSpec(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "example.com/tempo:dev" {
		t.Errorf("image with TEMPO_LOCALNET_IMAGE = %q", spec.Image)
	}
}

func TestFindComposeFile(t *testing.T) {
	root := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/tempoxyz/mpp-go\n\ngo 1.26\n")
	write("tests/go.mod", "module github.com/tempoxyz/mpp-go/tests\n")
	write("tests/tempotest/x.go", "package tempotest\n")
	want := filepath.Join(root, "docker-compose.yml")

	for _, dir := range []string{root, filepath.Join(root, "tests"), filepath.Join(root, "tests", "tempotest")} {
		got, err := findComposeFile(dir)
		if err != nil || got != want {
			t.Errorf("findComposeFile(%s) = %q, %v; want %q", dir, got, err, want)
		}
	}

	if got, err := findComposeFile(t.TempDir()); err == nil {
		t.Errorf("findComposeFile outside the repository = %q, want an error", got)
	}
}
