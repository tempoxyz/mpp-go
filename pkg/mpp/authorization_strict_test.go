package mpp

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatCredentialRejectsSerializationErrors(t *testing.T) {
	for _, credential := range []*Credential{nil, {Payload: map[string]any{"amount": math.NaN()}}, {Payload: map[string]any{"unsupported": make(chan int)}}} {
		_, err := FormatCredential(credential)
		require.Error(t, err)
	}
}
