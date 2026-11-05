# Security Policy

## Supported Versions

We support the latest release. Security fixes are applied to the current release only.

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |
| Older   | No        |

## Reporting a Vulnerability

**Do NOT file a public GitHub issue for security vulnerabilities.**

To report a vulnerability:

1. Open a [GitHub Security Advisory](https://github.com/apimgr/cairnbin/security/advisories/new) (preferred — keeps the report private until a fix is available)
2. Or contact the maintainers directly via the email listed on the GitHub profile

Please include:
- A description of the vulnerability
- Steps to reproduce
- Impact assessment (what an attacker could achieve)
- Any suggested fix or mitigation

## Expected Response

- Acknowledgment within 5 business days
- A fix or workaround within 30 days of confirmation (critical issues prioritized)
- Credit in the release notes if desired

## Disclosure Policy

We follow coordinated disclosure: please allow us time to fix the issue before making it public.

## Scope

This policy applies to the `cairnbin` binary and its Docker image hosted at `ghcr.io/apimgr/cairnbin`.

Out of scope:
- Vulnerabilities in paste content (operators are responsible for acceptable-use policies)
- Denial of service via large paste creation (rate limiting is in place; tune via config)
