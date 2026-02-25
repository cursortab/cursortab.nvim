# AGENTS.md

## Build & Test Commands

```bash
# Build the Go server
cd server && go build

# Run all tests
cd server && go test ./...

# Run tests for a specific package
cd server && go test ./text/...

# Run a single test
cd server && go test ./text/... -run TestDiff

# Check for dead code
cd server && deadcode .

# Run E2E pipeline tests (ComputeDiff → CreateStages → ToLuaFormat)
cd server && go test ./text/... -run TestE2E -v

# Record new expected output after changes
cd server && go test ./text/... -run TestE2E -update
```

Each E2E fixture is a `.txtar` file under `server/text/testdata/`. The header
contains cursor/viewport params, and sections contain `old.txt`, `new.txt`,
and `expected` (a custom DSL format). Both batch and incremental pipelines are
verified against the same expected output. Engine E2E fixtures are `.txtar`
files under `server/engine/testdata/` with `buffer.txt` and `steps` sections
(also a custom DSL format).

**Important:** Never run `-verify` or `-verify-case`. Verification must always
be done manually by the user.

When done with your changes run formatting with:

```bash
gofmt -w .
```

## Code Style

### No Legacy Code or Backward Compatibility

When refactoring or modifying code, completely remove old implementations. DO
NOT:

- Keep deprecated functions or methods
- Add backward-compatible shims or wrappers
- Leave commented-out old code
- Add comments explaining what changed from the old version
- Rename unused parameters with underscore prefixes

Treat the new code as if it was always the correct implementation.

## Bug Investigation

When working on bugs, follow this process:

1. **Trace logs with code** - If logs are provided, go line by line through the
   code path that produced them
2. **Find the root cause** - Don't stop at symptoms; understand why the bug
   occurs
3. **Write tests first** - Before fixing, write tests that validate your
   hypothesis about the root cause
4. **Fix and verify** - Apply the fix and confirm tests pass

## Testing Guidelines

### Test Behavior, Not Specific Bugs

Tests should verify general behavior, not be overly specific to a particular bug
scenario:

- Use generic code examples similar to existing tests in the codebase
- Test the behavior/contract of the function, not just the bug case
- Make tests readable and representative of real usage

### Use the Assert Package

Always use `server/assert/assert.go` for test assertions:

```go
import "cursortab/assert"

func TestExample(t *testing.T) {
    result := SomeFunction(input)
    assert.Equal(t, expected, result)
}
```
