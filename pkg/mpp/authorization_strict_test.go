package mpp

import (
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestFormatAuthorizationStrictRejectsSerializationErrors(t *testing.T) {
	for _, credential := range []*Credential{nil, {Payload: map[string]any{"amount": math.NaN()}}, {Payload: map[string]any{"unsupported": make(chan int)}}} {
		_, err := FormatAuthorizationStrict(credential)
		require.Error(t, err)
	}
}
