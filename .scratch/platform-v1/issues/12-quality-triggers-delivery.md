# 12: Quality triggers & Collection delivery

**What to build:** A Request becomes a completed, delivered Data Collection: the Agent gathers Member answers with quality triggers (completeness and anomaly checks, defined per Process Template — defaults until ticket 13), triggering re-asks or escalation on bad data; a Collection completes when triggers are met or the budget/timeout path runs (flagged incomplete). Delivery to the Consumer is anonymized (05) and streamed (04). The Consumer sees what's missing on incomplete Collections. Demoable: run a Collection with fake channels, inject bad answers, watch re-ask, get delivered anonymized output.

**Blocked by:** 05 (Anonymization pipeline), 11 (Channels, voice & durable pause).

**Status:** ready-for-agent

- [ ] Default quality triggers (completeness, anomaly) evaluate Member answers and trigger re-asks/escalation
- [ ] Collection completes via triggers met or budget/timeout path; incomplete Collections flagged with missing-data summary
- [ ] Delivery to Consumer is anonymized and entitlement-checked streaming
- [ ] Consumer-facing Collection detail with completeness status
- [ ] Tests: trigger firing, re-ask loop, completion paths, anonymized delivery (identity-leak asserts)
