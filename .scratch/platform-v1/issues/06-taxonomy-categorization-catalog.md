# 06: Taxonomy, auto-categorization & catalog

**What to build:** Platform Admins manage the categorization taxonomy (crop, region, growth stage, intervention, outcome, data type), seeded for Irish agriculture (counties; dairy/beef/tillage profile). Uploaded Assets are auto-categorized at ingest by a classifier; the owning Farmer Organization can correct categories. Data Consumers browse and search the catalog with taxonomy facets and see cached pricing. The taxonomy also stamps provenance metadata on data items. Demoable: upload → auto-categorized → findable by facet search as another user.

**Blocked by:** 03 (Credits Ledger & automatic pricing), 04 (Data Asset upload & org dashboard).

**Status:** ready-for-agent

- [ ] Admin CRUD on taxonomy; Irish seed data loads via migration/seed
- [ ] Classifier assigns categories at ingest; classification confidence/labels stored with the Asset
- [ ] Org can correct categories on their Assets
- [ ] Consumer catalog: browse, facet search by taxonomy, cached-price display
- [ ] Provenance fields (taxonomy-stamped) stored per data item
- [ ] Table-driven tests: categorization, correction, facet search, pricing display
