build:
    cd server && go build

test:
    cd server && go test ./...

test-e2e:
    cd server && go test ./text/... -run TestE2E -v

update-e2e:
    cd server && go test ./text/... -run TestE2E -update

verify-e2e test_case:
    cd server && go test ./text/... -run TestE2E -verify-case {{test_case}}

fmt:
    cd server && gofmt -w .

lint:
    cd server && deadcode .
