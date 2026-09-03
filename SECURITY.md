# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x   | Yes       |

## Reporting a Vulnerability

If you discover a security vulnerability in jLink, please report it responsibly.

### How to Report

1. **GitHub Security Advisory** (preferred): Use [GitHub's private security advisory feature](https://github.com/Watchdog0x/jLink/security/advisories/new)
2. **GitHub Issue**: Open an issue with the `[SECURITY]` prefix in the title

### What to Include

- jLink version affected
- Description of the vulnerability
- Steps to reproduce
- Impact assessment
- Suggested fix (if any)

## Response Timeline

- **Acknowledgment**: Within 48 hours
- **Initial Assessment**: Within 1 week
- **Fix Timeline**: Depends on severity

## Disclosure Policy

We follow coordinated disclosure:

1. Reporter submits vulnerability privately
2. We acknowledge and assess the report
3. We develop and test a fix
4. Fix is released
5. Public disclosure after the fix is available

## Jabra SDK Note

jLink depends on the proprietary Jabra SDK (`libjabra.so`) provided by GN Audio. Security vulnerabilities specific to the Jabra SDK should be reported directly to [GN Audio / Jabra](https://www.jabra.com/) rather than to this project.
