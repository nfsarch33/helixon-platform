# Contributing to Helixon Platform

Thank you for your interest in contributing to Helixon Platform.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/helixon-platform.git`
3. Create a branch: `git checkout -b feat/my-feature`
4. Make your changes following the guidelines below
5. Run tests: `go test -race -cover ./...`
6. Commit with a conventional commit message
7. Push and open a pull request

## Development Setup

### Prerequisites

- Go 1.22 or later
- No CGO required (pure Go SQLite via `modernc.org/sqlite`)

### Build and test

```bash
go build ./...
go test -race -cover ./...
go vet ./...
```

## Coding Guidelines

### Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `golangci-lint run` for additional checks
- Structured logging with `log/slog` (include `component` tag)
- Error wrapping: `fmt.Errorf("context: %w", err)`

### Testing

- Write tests first (TDD)
- All tests must pass with `-race` flag
- Maintain 75%+ coverage per package
- Use `testify/assert` and `testify/require`
- Use `httptest.NewServer` for HTTP tests
- Use `t.TempDir()` for filesystem tests

### Commits

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(fleet): add concurrent task handler
fix(agent): prevent session leak on timeout
docs(readme): add fleet integration examples
test(memory): improve hybrid searcher coverage
refactor(llm): extract streaming into separate file
```

### Pull Requests

- Keep PRs focused on a single change
- Include tests for new functionality
- Update documentation if adding new features or changing APIs
- Ensure CI passes (build, test, vet, coverage threshold)

## Project Structure

See [AGENTS.md](AGENTS.md) for the full project structure and key types.

## Reporting Issues

Use the GitHub issue templates:

- **Bug Report**: For bugs and unexpected behavior
- **Feature Request**: For new features and improvements

## License

By contributing, you agree that your contributions will be licensed under
the [MIT License](LICENSE).
