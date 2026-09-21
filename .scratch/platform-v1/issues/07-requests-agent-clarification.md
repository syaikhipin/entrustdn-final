# 07: Requests & agent clarification over web chat

**What to build:** A Data Consumer creates a Request — the data wanted, format, quality bar, budget in Credits — and the Agent clarifies it through a web-chat conversation (Seam 3 web Channel), using the model gateway (OpenAI-compatible, env-keyed; fake LLM in tests). Before fielding, the Agent checks the catalog and tells the Consumer when existing Assets already answer the need. Inference spend is metered and charged to the Ledger. The Consumer tracks Request status. Demoable: create Request → chat with the Agent → watch credits meter → see status change.

**Blocked by:** 06 (Taxonomy, auto-categorization & catalog).

**Status:** ready-for-agent

- [ ] Request creation: data description, format, quality bar, budget in Credits
- [ ] Web-chat Channel adapter drives the clarification conversation (round-trips through the Go↔Agent contract)
- [ ] Agent checks catalog first and reports existing-Asset matches
- [ ] Inference metered per token (input/output, cache-hit cheaper) and posted to the Ledger against the Request's budget
- [ ] Budget cannot be silently exceeded; exhaustion surfaces to the Consumer
- [ ] Request status view for the Consumer
- [ ] Tests at Seams 1–3 with fake LLM: clarification loop, catalog check, metering math, budget guard
