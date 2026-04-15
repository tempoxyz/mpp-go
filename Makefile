.PHONY: test integration

test:
	go test ./...

integration:
	@TEMPO_RPC_URL=$${TEMPO_RPC_URL:-http://localhost:8545} go test -tags=integration -run TestIntegration -timeout=5m ./tests
