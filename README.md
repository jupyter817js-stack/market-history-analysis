# Market history and strategy analysis

A Go/PostgreSQL research pipeline for ingesting public market activity and position data, preserving source records, normalizing them, and supporting replay, simulation and validation.

## Structure

| Directory | Purpose |
| --- | --- |
| cmd/rn1-ingest | Data ingestion CLI |
| cmd/rn1-query | Query CLI |
| cmd/rn1-replay | Replay entry point |
| internal/polymarket | Provider access |
| internal/scanner | Market scanning |
| internal/simulator | Simulation logic |
| internal/validation | Validation and settlement tracking |
| migrations | PostgreSQL schema |
| testdata/fixtures | Test inputs |

## Review locally

The module declares Go 1.22+. Use an isolated local environment.

~~~sh
go build ./...
go test ./...
~~~

Tests using local fixtures can be inspected without real account credentials. Commands requiring a database or network access need separate configuration. This edition includes source, migrations and fixtures; operational collection runs, private reports and tracked-account configuration are excluded.

## Boundaries

Historical analysis and simulation do not establish a strategy's future returns. No account access, live-order execution or performance claim was validated when preparing this snapshot. See VALIDATION.md for the actual checks.

## About this public edition

This is a source snapshot prepared for code review. Operational logs, account-specific configuration, runtime state, private keys, and the private repository history are excluded. The original implementation is preserved separately. This snapshot is not evidence of audited production readiness or investment performance.

Please use Upwork Messages for project discussions.
