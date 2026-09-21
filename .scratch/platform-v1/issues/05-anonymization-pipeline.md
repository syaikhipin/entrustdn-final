# 05: Anonymization pipeline

**What to build:** Real anonymization replaces the pass-through stage: personal identifiers in Data Assets are stripped or pseudonymized at ingest, and Collection outputs are anonymized before delivery to the Data Consumer (ADR 0005). Pseudonym maps (member → pseudonym) live only in-platform and never appear in delivered data. The defining tests are identity-leak assertions: feed uploads and Collection outputs containing names/phones/emails/field coordinates and assert none survive delivery.

**Blocked by:** 04 (Data Asset upload & org dashboard).

**Status:** ready-for-agent

- [ ] Ingest anonymization: known identifier types (names, phones, emails, coordinates) stripped/pseudonymized in structured and free-text fields
- [ ] Delivery anonymization: Collection outputs cleaned before the Consumer can retrieve them
- [ ] Pseudonym maps stored in-platform, never included in any export/delivery path
- [ ] Identity-leak tests: adversarial fixtures with identifiers must not survive to delivery
- [ ] Org dashboard delete of a pseudonym map cascades correctly (the GDPR erasure surface)
