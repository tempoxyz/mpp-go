package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// composeFile is the docker-compose.yml that pins the Tempo localnet image.
// It is the single source of truth for the image and its command: the live
// tests start the same container when TEMPO_RPC_URL is unset.
var composeFile = filepath.Join("..", "docker-compose.yml")

var (
	composeImagePattern   = regexp.MustCompile(`(?m)^\s*image:\s*\$\{TEMPO_LOCALNET_IMAGE:-([^}]+)\}\s*$`)
	composeCommandPattern = regexp.MustCompile(`(?m)^\s*command:\s*(\[.*\])\s*$`)
)

// localnetSpec is the tempo-node service of docker-compose.yml.
type localnetSpec struct {
	Image   string
	Command []string
}

// readLocalnetSpec reads the tempo-node image and command from
// docker-compose.yml. TEMPO_LOCALNET_IMAGE overrides the image, as it does for
// docker compose.
func readLocalnetSpec(path string) (localnetSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return localnetSpec{}, err
	}
	image := composeImagePattern.FindSubmatch(data)
	if image == nil {
		return localnetSpec{}, fmt.Errorf("%s: no image: ${TEMPO_LOCALNET_IMAGE:-...} line", path)
	}
	command := composeCommandPattern.FindSubmatch(data)
	if command == nil {
		return localnetSpec{}, fmt.Errorf("%s: no command: [...] line", path)
	}
	spec := localnetSpec{Image: strings.TrimSpace(string(image[1]))}
	if err := json.Unmarshal(command[1], &spec.Command); err != nil {
		return localnetSpec{}, fmt.Errorf("%s: command: %w", path, err)
	}
	if override := os.Getenv("TEMPO_LOCALNET_IMAGE"); override != "" {
		spec.Image = override
	}
	return spec, nil
}

func TestComposeLocalnetSpec(t *testing.T) {
	t.Setenv("TEMPO_LOCALNET_IMAGE", "")
	spec, err := readLocalnetSpec(composeFile)
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
	spec, err = readLocalnetSpec(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "example.com/tempo:dev" {
		t.Errorf("image with TEMPO_LOCALNET_IMAGE = %q", spec.Image)
	}
}
