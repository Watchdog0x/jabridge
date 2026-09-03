# Contributing to jLink

Thank you for your interest in contributing to jLink! This guide will help you get started.

## Table of Contents

- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [Making Changes](#making-changes)
- [Commit Messages](#commit-messages)
- [Pull Request Process](#pull-request-process)
- [Coding Standards](#coding-standards)
- [Reporting Bugs](#reporting-bugs)
- [Requesting Features](#requesting-features)

## Getting Started

1. Fork the repository on GitHub
2. Clone your fork locally:
   ```bash
   git clone https://github.com/<your-username>/jLink.git
   cd jLink
   ```
3. Add the upstream remote:
   ```bash
   git remote add upstream https://github.com/Watchdog0x/jLink.git
   ```

## Development Setup

### Prerequisites

- **Go** 1.23.2 or later
- **GCC** (C compiler for CGo)
- **xxd** (for extracting libjabra.so)
- **libasound2-dev** / **alsa-lib-devel** (ALSA development headers)
- **libcurl4-openssl-dev** / **libcurl-devel** (cURL development headers)

### Installing Dependencies

**Ubuntu/Debian:**
```bash
sudo apt-get install build-essential libasound2-dev libcurl4-openssl-dev xxd
```

**Fedora/RHEL:**
```bash
sudo dnf install gcc alsa-lib-devel libcurl-devel vim-common
```

**Arch Linux:**
```bash
sudo pacman -S base-devel alsa-lib curl xxd
```

### Building

```bash
# Extract the Jabra SDK library (first time only)
make extract-lib

# Build the binary
make build

# Or do both at once
make
```

### Running

```bash
# The binary needs libjabra.so in the library path
LD_LIBRARY_PATH=./lib ./jLink
```

### Linting and Formatting

```bash
# Run the linter
make lint

# Check code formatting
make fmt

# Run go vet
make vet
```

## Making Changes

1. Create a feature branch from `main`:
   ```bash
   git checkout main
   git pull upstream main
   git checkout -b feature/your-feature-name
   ```

2. Make your changes. Keep commits focused and atomic.

3. Ensure your code builds and passes lint checks:
   ```bash
   make
   make lint
   make fmt
   ```

4. Push your branch:
   ```bash
   git push origin feature/your-feature-name
   ```

## Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/). See our [Commit Convention](.github/COMMIT_CONVENTION.md) for full details.

**Quick reference:**
```
feat: add new feature
fix: resolve a bug
docs: update documentation
ci: change CI configuration
build: change build system
refactor: restructure code without changing behavior
```

## Pull Request Process

1. Update documentation if your changes affect user-facing behavior
2. Ensure CI passes (lint, build)
3. Fill in the [PR template](.github/PULL_REQUEST_TEMPLATE.md) completely
4. Request review from maintainers
5. Address review feedback with new commits (do not force-push during review)
6. Once approved, a maintainer will merge your PR

### PR Checklist

- [ ] Branch is based on latest `main`
- [ ] Code builds successfully (`make build`)
- [ ] Linter passes (`make lint`)
- [ ] Code is formatted (`make fmt`)
- [ ] Commit messages follow conventional commits
- [ ] Documentation is updated if needed

## Coding Standards

### Go Code

- Follow standard Go conventions and idioms
- Use `gofmt` for formatting (enforced by CI)
- Keep functions focused and reasonably sized
- Add comments for exported functions and complex logic
- Handle errors explicitly; do not ignore them
- Use meaningful variable and function names

### CGo Code

- All CGo interop is in `jabraApi.go`
- Free C-allocated memory using the appropriate Jabra SDK free functions
- Document unsafe pointer operations with comments explaining the cast

### TUI Code

- UI rendering logic is in `cmd.go`
- Use ANSI escape codes consistently
- Test UI changes with different terminal sizes

## Reporting Bugs

Use the [Bug Report template](https://github.com/Watchdog0x/jLink/issues/new?template=bug_report.yml) to report bugs. Please include:

- Your jLink version
- Your Linux distribution and kernel version
- The Jabra device(s) you are using
- Steps to reproduce the issue
- Expected vs actual behavior

## Requesting Features

Use the [Feature Request template](https://github.com/Watchdog0x/jLink/issues/new?template=feature_request.yml) to suggest new features. Check the [TODO list](README.md#todo) for already planned features.

## Code of Conduct

This project adheres to the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code.

## License

By contributing to jLink, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
