package challengemeta

import "testing"

func TestIsReserved(t *testing.T) {
	for _, key := range ReservedKeys {
		if !IsReserved(key) {
			t.Errorf("IsReserved(%q) = false", key)
		}
	}
	for _, key := range []string{"", "_mppx_scope", "plan"} {
		if IsReserved(key) {
			t.Errorf("IsReserved(%q) = true", key)
		}
	}
}
