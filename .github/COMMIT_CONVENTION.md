# Commit Convention

This project follows [Conventional Commits](https://www.conventionalcommits.org/) to maintain a clean and readable git history, and to enable automated changelog generation and semantic versioning.

## Format

```
<type>(<scope>): <description>

[optional body]

[optional footer(s)]
```

## Types

| Type | Description |
|------|-------------|
| `feat` | A new feature |
| `fix` | A bug fix |
| `docs` | Documentation only changes |
| `style` | Changes that do not affect the meaning of the code (white-space, formatting) |
| `refactor` | A code change that neither fixes a bug nor adds a feature |
| `perf` | A code change that improves performance |
| `test` | Adding missing tests or correcting existing tests |
| `build` | Changes that affect the build system or external dependencies |
| `ci` | Changes to CI configuration files and scripts |
| `chore` | Other changes that don't modify src or test files |
| `revert` | Reverts a previous commit |
| `deps` | Dependency updates |

## Scope (optional)

The scope provides additional context. Examples:

- `feat(bluetooth)`: A new Bluetooth feature
- `fix(battery)`: A battery monitoring fix
- `ci(release)`: A change to the release workflow
- `build(rpm)`: A change to RPM packaging

## Examples

```
feat: add firmware update support

fix(bluetooth): resolve pairing timeout with Jabra Link 380

docs: update installation instructions for Fedora

ci: add RPM and DEB package validation

build(debian): fix postinst script permissions

refactor(ui): simplify menu rendering logic

deps: bump golang.org/x/term from 0.27.0 to 0.28.0
```

## Breaking Changes

Breaking changes are indicated by a `!` after the type/scope or by including `BREAKING CHANGE:` in the footer:

```
feat!: change device configuration API

feat(bluetooth): rework pairing flow

BREAKING CHANGE: The pairing API now requires explicit device selection.
```

## Automated Usage

- **Changelog generation**: Commit types are used to categorize entries in the changelog
- **Version bumping**: `feat` triggers a minor version bump, `fix` triggers a patch bump, `!` or `BREAKING CHANGE` triggers a major bump
