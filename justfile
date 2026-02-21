build:
    cd server && go build

test *test_cases:
    cd server && go test ./... {{ if test_cases == "" { "" } else { "-run " + replace(test_cases, " ", "|") } }}

test-e2e *test_cases:
    cd server && go test ./text/... -run 'TestE2E{{ if test_cases == "" { "" } else { "/(" + replace(test_cases, " ", "|") + ")" } }}' -v

update-e2e *test_cases:
    cd server && go test ./text/... -run TestE2E {{ if test_cases == "" { "-update" } else { "$(for c in " + test_cases + "; do printf ' -update-only %s' \"$c\"; done)" } }}

verify-e2e +test_cases:
    cd server && go test ./text/... -run TestE2E $(for c in {{test_cases}}; do printf ' -verify %s' "$c"; done)

fmt:
    cd server && gofmt -w .

lint:
    cd server && deadcode .
