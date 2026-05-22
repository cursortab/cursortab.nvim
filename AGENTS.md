# AGENTS.md

@CONTRIBUTING.md

**Never run `just verify-e2e` (or `-verify`/`-verify-all` flags) on your own.**
It marks fixtures as human-reviewed. Stop after `update-e2e`. Run verify only
when the user explicitly asks.

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
