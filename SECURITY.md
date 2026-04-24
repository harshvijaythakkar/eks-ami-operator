# Security policy

## Supported versions

| Version | Supported |
|---|---|
| latest | ✅ |
| < latest | ❌ |

Security fixes are applied to the latest release only.

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Please report security vulnerabilities by emailing the maintainer directly. You can find contact information on the [maintainer's GitHub profile](https://github.com/harshvijaythakkar).

Include the following in your report:

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You will receive a response within 72 hours. If the issue is confirmed, a fix will be released as soon as possible.

## Security considerations for operators

When deploying eks-ami-operator:

- Use **IRSA** (IAM Roles for Service Accounts) instead of static AWS credentials
- Scope IAM permissions to specific cluster/nodegroup ARNs where possible
- The operator runs with `readOnlyRootFilesystem: true`, `runAsNonRoot: true`, and all Linux capabilities dropped
- Review the [Security & RBAC](README.md#security--rbac) section of the README