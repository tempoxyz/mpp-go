package tempo

import (
	"context"
	"testing"
)

func TestMemoryStoreOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(*testing.T, *MemoryStore)
	}{
		{
			name: "missing key",
			run: func(t *testing.T, store *MemoryStore) {
				t.Helper()
				if value, ok, err := store.Get(context.Background(), "missing"); err != nil || ok || value != "" {
					t.Fatalf("Get() = (%q, %t, %v), want (\"\", false, nil)", value, ok, err)
				}
			},
		},
		{
			name: "put and get",
			run: func(t *testing.T, store *MemoryStore) {
				t.Helper()
				if err := store.Put(context.Background(), "k", "v"); err != nil {
					t.Fatalf("Put() error = %v", err)
				}
				value, ok, err := store.Get(context.Background(), "k")
				if err != nil {
					t.Fatalf("Get() error = %v", err)
				}
				if !ok || value != "v" {
					t.Fatalf("Get() = (%q, %t), want (\"v\", true)", value, ok)
				}
			},
		},
		{
			name: "put if absent only inserts once",
			run: func(t *testing.T, store *MemoryStore) {
				t.Helper()
				inserted, err := store.PutIfAbsent(context.Background(), "k", "first")
				if err != nil {
					t.Fatalf("first PutIfAbsent() error = %v", err)
				}
				if !inserted {
					t.Fatal("first PutIfAbsent() = false, want true")
				}
				inserted, err = store.PutIfAbsent(context.Background(), "k", "second")
				if err != nil {
					t.Fatalf("second PutIfAbsent() error = %v", err)
				}
				if inserted {
					t.Fatal("second PutIfAbsent() = true, want false")
				}
				value, ok, err := store.Get(context.Background(), "k")
				if err != nil {
					t.Fatalf("Get() error = %v", err)
				}
				if !ok || value != "first" {
					t.Fatalf("Get() = (%q, %t), want (\"first\", true)", value, ok)
				}
			},
		},
		{
			name: "delete removes key",
			run: func(t *testing.T, store *MemoryStore) {
				t.Helper()
				if err := store.Put(context.Background(), "k", "v"); err != nil {
					t.Fatalf("Put() error = %v", err)
				}
				if err := store.Delete(context.Background(), "k"); err != nil {
					t.Fatalf("Delete() error = %v", err)
				}
				if value, ok, err := store.Get(context.Background(), "k"); err != nil || ok || value != "" {
					t.Fatalf("Get() after delete = (%q, %t, %v), want (\"\", false, nil)", value, ok, err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t, NewMemoryStore())
		})
	}
}

func TestStoreKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "charge key lowercases hash",
			got:  ChargeStoreKey("0xABC123"),
			want: "mppx:charge:0xabc123",
		},
		{
			name: "proof key prefixes challenge id",
			got:  ChargeProofStoreKey("challenge-1"),
			want: "mppx:charge:proof:challenge-1",
		},
		{
			name: "sponsored key prefixes challenge id",
			got:  ChargeSponsoredChallengeStoreKey("challenge-1"),
			want: "mppx:charge:sponsor:challenge-1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Fatalf("key = %q, want %q", tt.got, tt.want)
			}
		})
	}
}
