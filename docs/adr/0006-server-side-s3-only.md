# Server-side S3 only — clients never touch object storage

All uploads and downloads pass through the Go backend; the browser, the agent,
and Farmer Members' devices never receive S3 credentials, presigned URLs, or
direct bucket access. Storage is the self-hosted S3-compatible service on our
own server (per the original product notes).

The standard, more performant pattern is presigned-URL direct upload from
browser to bucket — we deliberately don't use it. Routing every byte through
the server is what makes Anonymization at ingest (ADR 0005) enforceable as a
pipeline stage rather than an uploader's discipline, and it keeps asset
authorization in one place: an S3 object is only reachable through a backend
request that checks entitlement. The bandwidth cost of proxying is accepted;
the backend streams (never buffers whole objects).

## Consequences

- Large dataset delivery is bounded by the backend's streaming, not the bucket's
  native throughput.
- External data sharing stays on the Connector path (API connections), never by
  handing out bucket paths.
- If direct upload is ever needed, it must not bypass the anonymization stage —
  that would be a new ADR, not an optimization.
