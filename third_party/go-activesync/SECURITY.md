# Security Policy

## Supported versions

go-activesync is pre-1.0 and ships from the `main` branch. Security
fixes land on `main` first; the most recent tagged release is patched
as a follow-up.

| Version | Supported          |
|---------|--------------------|
| latest `main` | :white_check_mark: |
| latest tagged release | :white_check_mark: |
| earlier tags | :x: (please upgrade) |

## Reporting a vulnerability

**Please do not file public issues for security bugs.**

Use GitHub's private vulnerability reporting:
<https://github.com/hstern/go-activesync/security/advisories/new>

Or email **henry@stern.ca**. Encrypt with the PGP key on
keys.openpgp.org for `henry@stern.ca` if the issue is sensitive.

Please include:

- A description of the issue and its impact
- Steps to reproduce (or a proof-of-concept) — including server type,
  EAS protocol version, and the auth scheme in play if relevant
- The affected commit / release
- Whether you've already disclosed publicly anywhere

I'll acknowledge receipt within **5 business days** and aim to publish
a fix and advisory within **30 days** of confirming the issue.
Coordinated disclosure is welcome — I'm happy to credit reporters in
the advisory unless you prefer otherwise.

## Scope

This policy covers the Go modules in this repository:

- `github.com/hstern/go-activesync/eas`
- `github.com/hstern/go-activesync/wbxml`

Issues in the testenv Docker stack (Z-Push / Dovecot / Postfix
configurations under `testenv/`) are out of scope unless they enable an
attack against the library itself; report those upstream.

## Threat model notes for triage

The library is a *client* of EAS servers; it parses **untrusted server
responses** and **handles user credentials**. Particular attention to
the WBXML decoder (an attacker who controls the server can supply
malformed input — fuzz-like behaviour matters), the Provision / policy
parsing path (attacker-controlled XML elements), and the auth-scheme
wrappers (token leakage, NTLM/Kerberos quirks).
