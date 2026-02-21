build:
    cd server && go build

test:
    cd server && go test ./...

test-e2e:
    cd server && go test ./text/... -run TestE2E -v

update-e2e:
    cd server && go test ./text/... -run TestE2E -update

verify-e2e +test_cases:
    cd server && go test ./text/... -run TestE2E $(for c in {{test_cases}}; do printf ' -verify %s' "$c"; done)

fmt:
    cd server && gofmt -w .

lint:
    cd server && deadcode .
