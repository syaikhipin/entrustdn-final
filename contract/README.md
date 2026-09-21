# Thresh Go↔Agent message contract

Defined once here; typed on both sides. This is the single source of truth for
what crosses the backend↔agent boundary (Seam 2 in the testing decisions).

- `schemas/envelope.schema.json` — the envelope every message rides in
- `schemas/ping-pair.schema.json` — the ping.request / ping.response pair the
  tracer bullet exercises
- `fixtures/*.json` — golden messages. The Go side pins its typed definitions to
  them in `backend/internal/contract/contract_test.go`; the Python side does the
  same in `agent/tests/test_contract.py`. Both suites also validate every fixture
  against the schemas above, so schema, fixtures, and typed definitions cannot
  drift apart silently.

Versioning: the envelope carries `version: 1`. Bump on breaking change; both
sides reject other versions. New message types (request.lifecycle,
channel.reply, …) land here as new payload schemas plus fixtures before either
side implements them.
