SOURCE_FILES?=$$(go list ./... | grep -v /vendor/)
TEST_PATTERN?=./...
TEST_OPTIONS?=-race

setup: ## Install all the build and lint dependencies
	sudo curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin v1.27.0
	GO111MODULE=off go get github.com/mfridman/tparse
	GO111MODULE=off go get golang.org/x/tools/cmd/cover
	go get -v -t ./...

test: ## Run all the tests
	go test $(TEST_OPTIONS) -covermode=atomic -coverprofile=coverage.txt -timeout=1m -cover -json $(SOURCE_FILES) | $$(go env GOPATH)/bin/tparse -all

integration: ## Run all the integration tests
	go test $(TEST_OPTIONS) -covermode=atomic -coverprofile=coverage.txt -integration -timeout=5m -cover -json $(SOURCE_FILES) | $$(go env GOPATH)/bin/tparse -top -all -dump

integration-ci: ## Run all the integration tests without any log and test dump
	go test $(TEST_OPTIONS) -covermode=atomic -coverprofile=coverage.txt -short -integration -timeout=5m -cover -json $(SOURCE_FILES) | $$(go env GOPATH)/bin/tparse -top -smallscreen -all

bench: ## Run the benchmark tests
	go test -bench=. $(TEST_PATTERN)

cover: integration ## Run all the tests and opens the coverage report
	go tool cover -html=coverage.txt

fmt: ## gofmt and goimports all go files
	find . -name '*.go' -not -wholename './vendor/*' | while read -r file; do gofmt -w -s "$$file"; goimports -w "$$file"; done

lint: ## Run all the linters
	golangci-lint run

# Absolutely awesome: http://marmelab.com/blog/2016/02/29/auto-documented-makefile.html
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
