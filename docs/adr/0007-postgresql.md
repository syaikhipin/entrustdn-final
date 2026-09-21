# PostgreSQL as the system of record

PostgreSQL is the sole relational store, remote-hosted by the user. The Ledger
demands ACID transactions and double-entry integrity; catalog, memberships,
Requests, Modules, taxonomy, and TOS acceptance records all fit relational
modeling; and pgvector can later host a vector backend behind a Memory Provider
without a second database. S3-compatible storage (ADR 0006) holds blobs; nothing
but references lives in object storage.

## Consequences

- Local dev runs the same schema against a reachable Postgres instance; env
  config points at the remote host the user installs.
- `DATABASE_URL`-style env config only; no embedded or file databases, so tests
  run against a disposable schema.
- ADR 0006 unchanged: presigned/direct database-adjacent shortcuts stay banned.
