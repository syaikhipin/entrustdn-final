# Thresh

*(Thresh — the agentic agricultural data-sharing platform.)*

A membership platform where Farmer Organizations share and sell agricultural data, and Data Consumers commission new data gathered from farmers by an AI agent. MCP is its connection fabric, and memory comes from pluggable external Memory Providers in the Hermes Agent style. It shares themes with the PhD thesis "Iterative Memory Routing for Agentic Agricultural Data Sharing with an MCP-Enabled Trust Layer", whose algorithm could later be connected as one more Memory Provider.

## Language

### People

**Farmer Organization**:
An organization of farmers that owns data on the platform, shares it, and receives a revenue share. The only party with a data-owner dashboard.
_Avoid_: farm group, farmer org, data provider

**Farmer Member**:
An individual farmer belonging to a Farmer Organization. Reached by the agent over a Channel; has no platform account.
_Avoid_: localized farmer, respondent

**Data Consumer**:
A scientist, government analyst, or other paying party who commissions or buys data.
_Avoid_: user, client, buyer

**Platform Admin**:
The operator who controls modules, models, pricing, and memberships.
_Avoid_: super admin

**Module Author**:
Whoever creates and uploads a Module; may hold any other role.

### Data

**Data Asset**:
An existing dataset or document a Farmer Organization has uploaded as inventory; priced as cached data. Stored server-side only — clients never touch object storage directly.
_Avoid_: cached data, upload, dataset (ambiguous with Collection)

**Data Collection**:
The dataset produced on demand by a completed Request, gathered from Farmer Members by the agent.
_Avoid_: survey, result

**Request**:
A Data Consumer's commission — existing Assets and/or a new Collection — with format, quality bar, and a budget in credits.
_Avoid_: order, task, job

**Provenance**:
The verifiable origin metadata attached to every data item and memory trace — who contributed, when, from which source — used as a trust dimension in ranking.
_Avoid_: audit trail, lineage, history

**Taxonomy**:
The Platform Admin's controlled vocabulary for describing Data Assets: six fixed axes — crop, region, growth stage, intervention, outcome, data type — each holding terms with classifier keywords. Seeded for Irish agriculture; term identity (axis and slug) is immutable once created.
_Avoid_: category list, tagging scheme, ontology

**Assignment**:
One taxonomy stamp on a Data Item: the term, its denormalized label, the confidence, and the source — `classifier` (machine, at ingest) or `org` (the owning Farmer Organization's correction, confidence 1). The org's correction replaces the whole set: it is the truth, not a patch.
_Avoid_: tag, label (ambiguous with the term's label), category (ambiguous with the axis)

**Catalog**:
The Data Consumer's read view across every Farmer Organization's shared Data Assets: entries with their Assignments, facet counts computed from the live inventory, and each asset's cached price from the current price book. Facet filtering happens client-side against those counts.
_Avoid_: marketplace, listings, store

### Memory

**Memory Provider**:
An external memory service the Agent connects to over MCP — e.g. mem0, Hindsight, supermemory, Honcho. Admin-configured, swappable, several may be connected at once.
_Avoid_: memory layer, built-in memory, brain

### Agent & Modules

**Agent**:
The AI actor that clarifies Requests, converses with Farmer Members over Channels, and remembers via connected Memory Providers.
_Avoid_: bot, assistant, AI

**Channel**:
A messaging medium between the agent and a Farmer Member: WhatsApp, Telegram, email, web, or voice. The web Channel (ticket 07) is the platform's own chat surface between the Agent and a Data Consumer.
_Avoid_: integration

**Model Gateway**:
The OpenAI-compatible inference endpoint the Agent's clarification loop calls, configured by environment. Distinct from a Channel (a medium to people) and from the Payment Gateway (platform configuration).
_Avoid_: LLM provider, API

**Module**:
A versioned, uploadable bundle — manifest plus markdown and/or configuration — of exactly one kind: Agent Skill, Process Template, or Connector. System-wide or private.
_Avoid_: plugin, extension, app

**Agent Skill**:
A Module of instructions the agent loads to perform a specialized task.
_Avoid_: agents.md, prompt

**Process Template**:
A Module defining a reusable Request workflow: questions, follow-up rules, and quality triggers.
_Avoid_: form, survey template

**Connector**:
A Module configuring an MCP or API link to an external data source.
_Avoid_: integration, data source

### Money

**Credits**:
The internal unit of value. Budgets, prices, and revenue shares are denominated in credits; amounts are set automatically by pricing rules and editable by the Platform Admin.
_Avoid_: tokens (as currency), payment, cash

**Top-up**:
Converting external money into Credits through the connected Payment Gateway.
_Avoid_: deposit, recharge

**Payment Gateway**:
An admin-configured external payment service (e.g. Stripe, PayPal) connected by API key. Platform configuration, not a user-uploadable Module.
_Avoid_: payment provider, PSP

**Ledger**:
The double-entry record of every credit movement, including Top-ups, Revenue Shares, and admin adjustments.
_Avoid_: billing, transactions

**Revenue Share**:
The split of a Request's data premium between the Farmer Organization and the platform.
_Avoid_: payout, commission

### Agreements & Privacy

**Terms of Service**:
The platform contract every registrant accepts at sign-up; versioned, with the accepted version recorded per account.
_Avoid_: contract, user agreement, TOS

**Anonymization**:
Stripping or pseudonymizing personal data from Data Assets at ingest, and from Collection outputs before delivery, so shared data cannot identify a Farmer Member.
_Avoid_: masking, scrubbing, GDPR (broader than this)
