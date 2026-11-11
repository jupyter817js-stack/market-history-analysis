# Validation and limitations

Checked: 11 November 2026. These results describe this public source snapshot only.

- go build ./...: passed using Go 1.27.4 on Windows.
- go test ./...: passed.
- Database-dependent coverage may be skipped without configured integration services. No live collection, production database or deployment validation was performed.
- Dependency vulnerability scanning was not performed for this Go module.

## Publication review

Private operational files and original history are excluded. Source was reviewed for credentials and direct contact information; the new public Git history is scanned with Gitleaks before publication. Narrow inline exemptions identify deterministic test fixtures and public contract addresses, not operational secrets. Automated checks are not a security audit and cannot guarantee the absence of vulnerabilities.
