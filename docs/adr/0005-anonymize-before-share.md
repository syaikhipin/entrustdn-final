# Anonymize before share

Every Data Asset is anonymized at ingest and every Collection output is
anonymized before delivery to the Data Consumer: personal identifiers are
stripped or pseudonymized so shared data cannot identify a Farmer Member.
Pseudonym maps (member → pseudonym) stay inside the Farmer Organization's
boundary in the platform database and are never included in delivered data.
This is a boundary decision: privacy is enforced by the pipeline, not left to
uploaders' discipline, and delivery-side anonymization is what makes "shared"
mean safe across the whole catalog.

## Consequences

- A shipped dataset that could identify a member would be a platform bug, not an
  uploader's mistake.
- The pseudonym map is the one dataset the org dashboard cannot export or delete
  casually — it is the GDPR erasure surface (linkable to [[erasure]] when designed).
- Re-identification risk is reduced, not eliminated: rare combinations of
  region + crop + growth stage can be identifying; k-anonymity-style checks may
  be added later before delivery.
