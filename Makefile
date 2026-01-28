test:
	GO_ENV=test go test ./...
lint:
	pre-commit run golangci-lint --all-files
	pre-commit run check-yaml --all-files
	pre-commit run helmlint --all-files
version:
	pre-commit run update-chart-version --all-files
release:
	./scripts/release.sh
