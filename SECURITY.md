# Security Policy

## Reporting a vulnerability

Please do not report security vulnerabilities in public issues.

Use [GitHub private vulnerability reporting](https://github.com/october-dev/moirai/security/advisories/new) to send the maintainers a private report. Include the affected component, impact, reproduction steps, and any suggested fix.

We will acknowledge a complete report as soon as practical and keep you informed while it is investigated.

## Current support

Moirai is in its design stage and has no stable release. Security fixes will be applied to the latest prerelease and the default branch.

## Relevant reports

Useful reports include:

- disclosure of session data or secrets;
- access to a session outside its granted scope;
- unsafe archive import, path traversal, or code execution;
- cross-session data leakage;
- authentication or authorization bypasses;
- corruption of session ancestry or checkpoint integrity;
- denial of service from untrusted session data;
- failures in encryption or redaction behavior once implemented.

Do not use real private sessions, credentials, or customer data when demonstrating a vulnerability.
