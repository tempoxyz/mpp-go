package tempo

import "testing"

func TestParseHexUint64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    uint64
		wantErr bool
	}{
		{name: "with prefix", input: "0x1a", want: 26},
		{name: "without prefix", input: "ff", want: 255},
		{name: "zero", input: "0x0", want: 0},
		{name: "bare 0x decodes to zero", input: "0x", want: 0},
		{name: "empty decodes to zero", input: "", want: 0},
		{name: "invalid", input: "0xzz", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHexUint64(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseHexUint64(%q) = %d, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHexUint64(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ParseHexUint64(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseHexZeroValueParity locks in that both hex decoders agree on the
// zero-value forms a lenient JSON-RPC node might return.
func TestParseHexZeroValueParity(t *testing.T) {
	for _, input := range []string{"0x", ""} {
		u, err := ParseHexUint64(input)
		if err != nil || u != 0 {
			t.Fatalf("ParseHexUint64(%q) = (%d, %v), want (0, nil)", input, u, err)
		}
		b, err := ParseHexBigInt(input)
		if err != nil || b.Sign() != 0 {
			t.Fatalf("ParseHexBigInt(%q) = (%v, %v), want (0, nil)", input, b, err)
		}
	}
}
