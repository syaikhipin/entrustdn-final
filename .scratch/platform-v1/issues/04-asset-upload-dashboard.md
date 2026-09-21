# 04: Data Asset upload & org dashboard

**What to build:** A Farmer Organization uploads datasets/documents as Data Assets through the backend, which streams them to S3-compatible storage server-side (ADR 0006 — clients never see bucket credentials or presigned URLs; local MinIO in dev), records metadata (name, description, size, format, provenance fields), and shows every shared Asset on the org's shared-data dashboard, where the org can update metadata or delete an Asset (deleting removes object + record). Anonymization runs as a pass-through pipeline stage at ingest in this ticket (real stripping lands in 05). Demoable: upload a CSV via the UI, see it in the dashboard, delete it.

**Blocked by:** 02 (Membership, Terms of Service & roles).

**Status:** ready-for-agent

- [ ] Upload streams through the backend to S3-compatible storage; no presigned URLs, no client bucket access
- [ ] Download/delivery streams through the backend with entitlement check
- [ ] Dashboard lists the org's Assets; update metadata and delete work (delete removes object and record)
- [ ] Anonymization stage exists in the ingest pipeline as pass-through
- [ ] Table-driven tests: upload, streaming, entitlement denial, delete
