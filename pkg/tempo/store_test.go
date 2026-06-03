package tempo

import (
	"context"
	"github.com/stretchr/testify/assert"
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
				{
					value, ok, err := store.Get(context.Background(), "missing")
					if !assert.Falsef(t, err != nil || ok || value != "",
						"Get() = (%q, %t, %v), want (\"\", false, nil)", value, ok, err) {
						return
					}
				}

			},
		},
		{
			name: "put and get",
			run: func(t *testing.T, store *MemoryStore) {
				t.Helper()
				{
					err := store.Put(context.Background(), "k", "v")
					if !assert.NoErrorf(t, err,
						"Put() error = %v", err) {
						return
					}
				}

				value, ok, err := store.Get(context.Background(), "k")
				if !assert.NoErrorf(t, err,
					"Get() error = %v", err) {
					return
				}
				if !assert.Falsef(t, !ok || value != "v",
					"Get() = (%q, %t), want (\"v\", true)", value, ok) {
					return
				}

			},
		},
		{
			name: "put if absent only inserts once",
			run: func(t *testing.T, store *MemoryStore) {
				t.Helper()
				inserted, err := store.PutIfAbsent(context.Background(), "k", "first")
				if !assert.NoErrorf(t, err,
					"first PutIfAbsent() error = %v", err) {
					return
				}
				if !assert.True(t, inserted,
					"first PutIfAbsent() = false, want true") {
					return
				}

				inserted, err = store.PutIfAbsent(context.Background(), "k", "second")
				if !assert.NoErrorf(t, err,
					"second PutIfAbsent() error = %v", err) {
					return
				}
				if !assert.False(t, inserted,
					"second PutIfAbsent() = true, want false") {
					return
				}

				value, ok, err := store.Get(context.Background(), "k")
				if !assert.NoErrorf(t, err,
					"Get() error = %v", err) {
					return
				}
				if !assert.Falsef(t, !ok || value != "first",
					"Get() = (%q, %t), want (\"first\", true)", value, ok) {
					return
				}

			},
		},
		{
			name: "delete removes key",
			run: func(t *testing.T, store *MemoryStore) {
				t.Helper()
				{
					err := store.Put(context.Background(), "k", "v")
					if !assert.NoErrorf(t, err,
						"Put() error = %v", err) {
						return
					}
				}
				{

					err := store.Delete(context.Background(), "k")
					if !assert.NoErrorf(t, err,
						"Delete() error = %v", err) {
						return
					}
				}
				{

					value, ok, err := store.Get(context.Background(), "k")
					if !assert.Falsef(t, err != nil || ok || value != "",
						"Get() after delete = (%q, %t, %v), want (\"\", false, nil)", value, ok, err) {
						return
					}
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
			if !assert.Equalf(t, tt.want, tt.got,
				"key = %q, want %q", tt.got, tt.want) {
				return
			}

		})
	}
}
