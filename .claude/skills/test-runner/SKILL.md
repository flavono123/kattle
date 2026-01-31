---
name: test-runner
description: Run Go and frontend tests for kattle project. Use after code changes to verify correctness.
disable-model-invocation: true
user-invocable: true
allowed-tools: Bash, Read
---

# Test Runner Skill

This skill provides commands and workflows for running tests in the kattle project across Go backend and React frontend codebases.

## Go Tests

Run backend tests using the Go test runner:

```bash
# All tests
go test ./...

# Specific package
go test ./internal/kube/...

# With verbose output
go test -v ./...

# With race detection
go test -race ./...

# With coverage
go test -cover ./...
```

### Options Reference
- `-v`: Verbose output with individual test results
- `-race`: Enable race condition detection
- `-cover`: Display coverage percentage
- `-timeout`: Set timeout (e.g., `-timeout 30s`)
- `-run`: Run specific tests matching pattern (e.g., `-run TestName`)

## Frontend Tests

Run frontend tests using npm in the React project:

```bash
# Run all tests (single run)
npm --prefix cmd/gui/frontend run test:run

# Watch mode (re-run on file changes)
npm --prefix cmd/gui/frontend run test

# With coverage report
npm --prefix cmd/gui/frontend run test:run -- --coverage
```

### Frontend Test Scripts
- `test`: Run tests in watch mode for development
- `test:run`: Single test run (useful for CI/CD)
- Coverage reports are generated in `coverage/` directory

## Combined Test Run

Run both Go and frontend tests in sequence:

```bash
# Quick validation (both suites)
go test ./... && npm --prefix cmd/gui/frontend run test:run
```

This validates the entire codebase before committing or creating a PR.

## Report Format

After running tests, expect output with:

- **PASS/FAIL status**: Clear indication of test suite success or failure
- **Failed test details**: Names and error messages for any failing tests
- **Coverage summary**: If coverage flags are used, percentage of code covered
- **Execution time**: Total time taken to run test suite

## Recommended Test Workflow

1. **After making changes**: Run targeted tests first
   ```bash
   go test ./internal/kube/...
   ```

2. **Before committing**: Run full test suites
   ```bash
   go test ./... && npm --prefix cmd/gui/frontend run test:run
   ```

3. **For pull requests**: Include coverage results
   ```bash
   go test -cover ./... 
   npm --prefix cmd/gui/frontend run test:run -- --coverage
   ```
