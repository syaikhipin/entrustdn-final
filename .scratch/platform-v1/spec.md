# Spec: Agentic Agricultural Data Sharing Platform — v1

Status: ready-for-agent
Tracker: local markdown, `.scratch/platform-v1/`
Vocabulary: `CONTEXT.md` (canonical terms used throughout). Decisions: ADRs 0001–0006 in `docs/adr/`.

## Problem Statement

Farmer Organizations across Europe sit on agricultural data — existing datasets and the field knowledge of their Farmer Members — that scientists and government analysts need but cannot reach. There is no trusted venue where a Data Consumer can find existing data, or commission new data gathered directly from farmers, with clear terms, quality expectations, provenance, and payment. Farmers have no way to earn from what they know, and no protection for their identity when they share.

## Solution

A membership platform, piloted in Ireland. Farmer Organizations register, accept the Terms of Service, upload existing data as Data Assets (anonymized at ingest), and see everything they share on a dashboard they control (update or delete). Data Consumers register, top up Credits via a pluggable Payment Gateway (Stripe or PayPal by admin-configured API key), browse the catalog, and create Requests that state the data they want, its format, quality bar, and a budget in Credits. An AI Agent clarifies the Request with the Consumer, then gathers new data by conversing with Farmer Members over Channels (WhatsApp first, also Telegram, email, and voice notes transcribed via STT) — conversations that pause for days and resume mid-thread. Collections complete when quality triggers are met or resumable links expire. Delivery is anonymized. Every credit movement — metered inference, cached-Asset pricing, unique-Collection premium, Revenue Share (80/20 org/platform default, pro-rata within the org) — posts to a double-entry Ledger. Platform Admins manage the taxonomy that drives auto-categorization, the model gateway (OpenAI-compatible, key in env), Memory Providers (external MCP services, Hermes-style), Modules (Agent Skills, Process Templates, Connectors — markdown, system-wide or private), credit overrides, and platform stats dashboards. All connections ride the MCP spine; all storage is a self-hosted S3-compatible service accessed server-side only.

## User Stories

### Registration & Membership

1. As a Data Consumer, I want to register with email verification, so that my account is provably mine.
2. As a Data Consumer, I want to accept the versioned Terms of Service at registration, so that I know which contract governs my use.
3. As a Farmer Organization, I want to register with email verification and accept the Terms of Service, so that my organization can share data under a clear contract.
4. As a Platform Admin, I want new accounts to verify via emailed links, so that membership is trustworthy (in dev, verification links land in the server log).
5. As a Farmer Organization, I want to invite my Farmer Members' contact points into the platform, so that the Agent can reach them over Channels without them needing accounts.
6. As a Platform Admin, I want to approve or reject Farmer Organization applications, so that only genuine organizations trade on the platform.
7. As any registrant, I want to see which TOS version I accepted and when, so that I can prove what I agreed to.
8. As a Platform Admin, I want to publish a new TOS version and require re-acceptance at next login, so that contract changes propagate.

### Catalog & Data Assets

9. As a Farmer Organization, I want to upload datasets and documents as Data Assets, so that they become sellable inventory.
10. As a Farmer Organization, I want my uploads anonymized at ingest automatically, so that no Farmer Member can be identified from what I share.
11. As a Farmer Organization, I want uploads stored server-side only, so that no client ever touches object storage directly.
12. As a Farmer Organization, I want my Assets auto-categorized against the taxonomy (crop, region, growth stage, intervention, outcome, data type), so that buyers can find them.
13. As a Farmer Organization, I want to correct auto-categorization on my Assets, so that mislabels don't misrepresent my data.
14. As a Farmer Organization, I want a shared-data dashboard listing everything I've shared, so that I have one view of my inventory.
15. As a Farmer Organization, I want to update or delete my Assets from the dashboard, so that I stay in control of what's shared.
16. As a Data Consumer, I want to browse and search the Asset catalog by taxonomy facets, so that I can find existing data fast.
17. As a Data Consumer, I want cached Assets priced as cached data (cheaper), so that I'm not paying premium for what already exists.
18. As a Data Consumer, I want to preview Asset metadata (schema, coverage, quality, provenance) before buying, so that I know what I'm getting.
19. As a Data Consumer, I want delivery of a purchased Asset streamed through the platform, so that access stays authorized and anonymized.

### Requests & the Agent

20. As a Data Consumer, I want to create a Request stating the data I want, its format, quality bar, and budget in Credits, so that the Agent knows what to gather.
21. As a Data Consumer, I want the Agent to ask me clarifying questions before fielding the Request, so that my spend matches my real need.
22. As a Data Consumer, I want the Agent to check catalog Assets first and tell me when existing data already answers my need, so that I don't commission redundant Collections.
23. As a Data Consumer, I want to track my Request's progress, so that I know when data will arrive.
24. As a Data Consumer, I want to receive the completed Collection anonymized, so that no Farmer Member is identifiable in what I paid for.
25. As the Agent, I want to load Agent Skill Modules for the task at hand, so that I follow domain-correct collection procedures.
26. As the Agent, I want to run Process Template Modules defining questions, follow-up rules, and quality triggers, so that Collections meet their quality bars.
27. As the Agent, I want to converse with a Farmer Member over WhatsApp, so that I gather data where the farmer already is.
28. As the Agent, I want to converse over Telegram, email, and web chat as alternative Channels, so that a Member isn't excluded by channel choice.
29. As the Agent, I want to receive voice notes, transcribe them via STT, and reply in text, so that low-literacy or busy Members can still contribute.
30. As the Agent, I want a conversation to pause for days awaiting a Member's reply and resume mid-thread, so that data collection respects farming schedules.
31. As the Agent, I want to remember across sessions via admin-configured Memory Providers over MCP, so that I don't re-ask what I already know.
32. As the Agent, I want to query external data sources through Connector Modules over MCP, so that I can enrich or cross-check answers.
33. As a Farmer Member, I want to answer the Agent from my phone over a Channel I already use, so that contributing costs me no new tooling.
34. As a Farmer Member, I want to receive a resumable link after a partial answer, so that I can finish my contribution later without restarting.
35. As a Farmer Member, I want the Agent's questions to stop when quality triggers are met, so that I'm not over-surveyed.
36. As a Farmer Organization, I want to see my Members' participation in my Collections, so that I can allocate Revenue Share fairly.

### Data Quality

37. As a Data Consumer, I want incomplete Collections flagged with what's missing, so that I know whether quality triggers were met or the budget/timeout path ran.
38. As the Agent, I want to trigger re-asks or escalation on anomalous or contradictory answers, so that Collection quality holds without human babysitting.
39. As a Platform Admin, I want quality-trigger rules defined per Process Template, so that quality bars are explicit, not vibes.
40. As a Farmer Organization, I want collection failures to surface on my dashboard, so that I can fix member-side problems.

### Money

41. As a Data Consumer, I want to top up Credits through Stripe or PayPal, so that I can fund Requests with real money.
42. As a Data Consumer, I want inference metered per token (input/output, cache-hit cheaper), so that my Agent spend is transparent.
43. As a Data Consumer, I want a single Request cost breakdown (inference, cached data, unique data), so that I can audit what I paid.
44. As a Farmer Organization, I want my Revenue Share (80/20 default, pro-rata by Member contribution) posted to the Ledger, so that my earnings are accountable.
45. As a Platform Admin, I want credit amounts set automatically by pricing rules, so that consistency doesn't depend on manual entry.
46. As a Platform Admin, I want to override any credit amount or grant Credits, so that I can handle exceptions and pilot participants.
47. As a Platform Admin, I want Revenue Share percentages admin-tunable, so that commercial terms can evolve.
48. As a Farmer Organization, I want to accrue Credits in my org account (no cash payouts in v1), so that my share accumulates until payout integration exists.

### Modules

49. As a Module Author, I want to upload a versioned Module (manifest + markdown/config) of one kind — Agent Skill, Process Template, or Connector — so that the platform grows without core changes.
50. As a Module Author, I want my Module private by default, so that I control who uses it.
51. As a Platform Admin, I want to promote Modules to system-wide, so that the best procedures become defaults.
52. As a Platform Admin, I want to review Module content before promotion, so that nothing harmful becomes a default.
53. As any member, I want to grant or revoke access to my private Modules, so that sharing is deliberate.
54. As a Data Consumer, I want to attach my private Process Template to a Request, so that collection follows my procedure.

### Admin & Dashboards

55. As a Platform Admin, I want to configure the model gateway (OpenAI-compatible base URL, model set, keys in env), so that inference routes through chosen providers.
56. As a Platform Admin, I want to connect Memory Providers by MCP endpoint, so that memory is swappable per deployment.
57. As a Platform Admin, I want platform stats (Requests, Collections, Credits flow, Channel activity) on a dashboard, so that I can see system health at a glance.
58. As a Farmer Organization, I want a stats view of my Assets' sales and my Members' participation, so that I can manage my data business.
59. As a Data Consumer, I want a spend dashboard, so that I can watch my Credits.
60. As a Platform Admin, I want Anonymization to run as an enforceable pipeline stage at ingest and before delivery, so that privacy is structural, not optional.

### Thesis Showcase (shown, not built)

61. As a visitor, I want a showcase page presenting the thesis's published results (Iterative Routing 50/50 vs 43/50, Hybrid Memory 0.753, MemoryGraph 97.0%, etc.), so that I can see the research behind the platform.
62. As a visitor, I want the A2A seam documented as a capability description in Module manifests, so that future agent-to-agent interop is visible without runtime support.

## Implementation Decisions

- **Monorepo, three services**: `backend/` (Go — system of record: auth, membership, catalog, Requests, Ledger, anonymization, storage, admin), `agent/` (Python + LangGraph sidecar — agentic loop, Channels, STT, MCP client), `web/` (Nuxt.js). ADR 0001.
- **Agent runtime**: LangGraph, chosen for durable execution — checkpointed state survives multi-day Channel pauses and resumes mid-thread. Backend↔agent communication is typed messages (request lifecycle events, channel replies) over HTTP/queue; contract-first, both sides testable against fakes.
- **Memory**: no built-in memory. External Memory Providers (mem0, Hindsight, supermemory, Honcho, …) connected over MCP, admin-configured, swappable. ADR 0003.
- **Connection fabric**: MCP everywhere the agent reaches out — Memory Providers and Connector Modules alike. A2A is a documented manifest capability only, no runtime. Connector-mode data sharing is API-connection based; uploaded data always goes to self-hosted S3-compatible storage, server-side only (ADR 0006) — streaming proxy in the backend, never presigned URLs or client bucket access.
- **Anonymization**: pipeline stage at ingest and before delivery; pseudonym maps stay in-platform and never ship. ADR 0005.
- **Money**: Credits in a double-entry Ledger; automatic pricing (per model, cached vs unique) with admin override; Revenue Share 80/20 org/platform default, admin-tunable, pro-rata within org by Member contribution; Top-ups via pluggable Payment Gateway (Stripe/PayPal, admin-configured API key); no cash payouts in v1. ADRs 0002, 0004.
- **Membership**: email verification required; dev mode logs links instead of sending (no real mail server at pilot). Versioned TOS with per-account acceptance records.
- **Auto-categorization**: admin-managed taxonomy (crop, region, growth stage, intervention, outcome, data type) seeded for Irish agriculture; classifier assigns at ingest; Farmer Organizations can correct. The same taxonomy drives catalog facets and episodic-style provenance metadata.
- **Voice**: voice notes over Channels transcribed via STT through the model gateway (API key in env, agent-side); replies in text. No live voice calls.
- **Model gateway**: OpenAI-compatible base URL + custom model set configured by admin; keys in env (STT included).
- **Modules**: versioned bundles — manifest + markdown and/or config — of exactly three kinds (Agent Skill, Process Template, Connector); visibility system-wide or private with grants. No executable plugins; markdown-defined only.
- **Thesis showcase**: static presentation of published results; no thesis code in the product.

## Testing Decisions

- **What makes a good test here**: assert external behavior at the agreed seams only — never internals. A test that breaks when message field order changes is a bad test; one that breaks when a Farmer Member's identity leaks into delivered data is a good test.
- **Seam 1 — Backend public HTTP API (primary)**: nearly all Go tests are table-driven suites driving the API with storage/gateway doubles: registration + TOS, catalog, Requests, auto-categorization, Ledger postings and Revenue Share math, anonymized delivery (identity-leak assertions), top-up callbacks, admin operations. Prior art: none in-repo (greenfield); conventions per repo Go rules (table-driven, `-race`, coverage per commit).
- **Seam 2 — Go ↔ Agent message contract**: contract-fake agent tests the backend's lifecycle side; contract-fake backend tests the sidecar's; one thin integration suite runs both real processes for the pause-for-days/resume path.
- **Seam 3 — Channel adapter interface (agent-side)**: WhatsApp/Telegram/email/web each implement one adapter interface; tests drive fake channels; STT faked behind env-keyed client. Email asserts on the dev log sink.
- **Seam 4 — MCP client boundary (agent-side)**: Memory Providers and Connectors faked as in-process MCP servers; assert the agent's recall/connector behavior against them.
- **Cross-cutting**: no test touches live WhatsApp/Telegram/Stripe/PayPal/S3/LLM APIs. The tracer-bullet ticket proves real process wiring once; thereafter everything rides the seams.

## Out of Scope

- Cash payouts to Farmer Organizations (accrual only; payout ADR later).
- Real email server at pilot (dev logs links).
- Executable/Joomla-style server-side plugins (markdown Modules only).
- Thesis algorithms in-product (showcase page only); Iterative Routing may someday connect as an MCP Memory Provider, not v1 work.
- Blockchain/Merkle provenance, EWC seasonal retention, differentiable retrieval, live voice calls, dual-agent Fast Talker/Slow Thinker split, benchmark dashboards beyond static showcase numbers.
- A2A runtime.
- k-anonymity-style re-identification checks (noted in ADR 0005 as future).
- GDPR erasure workflows beyond org dashboard delete (pseudonym map is the designed surface; full erasure flow is later work).
- Multi-language UI/agent beyond English (Ireland pilot).
- Mobile apps.

## Further Notes

- Pilot facts: European market, Ireland first; one test Farmer Organization before any named real org; WhatsApp-first channel priority.
- Credit amounts: automatic pricing rules are the default path; admin override exists for pilot exceptions — both write through the Ledger.
- Funding context (EU MSCA ENTRUST-DN, grant 101073381) motivates the showcase page but constrains nothing technical.
- Seams confirmed with the user 2026-09-21; STT confirmed env-keyed and agent-side.
