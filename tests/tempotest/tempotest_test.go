package tempotest

import (
	"strings"
	"testing"
)

func TestReadSpec(t *testing.T) {
	t.Setenv("TEMPO_LOCALNET_IMAGE", "")
	spec, err := ReadSpec(ComposeFile())
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
	spec, err = ReadSpec(ComposeFile())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "example.com/tempo:dev" {
		t.Errorf("image with TEMPO_LOCALNET_IMAGE = %q", spec.Image)
	}
}
