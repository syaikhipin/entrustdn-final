# 13: Agent consumes Modules

**What to build:** Modules stop being inert registry entries and start driving the Agent: Agent Skills load into the agent's context for specialized tasks, and Process Templates (questions, follow-up rules, quality triggers) replace the default Collection flow when attached to a Request. A Data Consumer can attach their own private Process Template. Demoable: two Requests with different Process Templates run visibly different collection conversations.

**Blocked by:** 08 (Module registry), 12 (Quality triggers & Collection delivery).

**Status:** ready-for-agent

- [ ] Agent Skills (markdown) load into agent context when authorized
- [ ] Process Templates drive Collections: questions, follow-up rules, triggers from the template
- [ ] Consumer can attach a private Process Template to their Request
- [ ] Authorization respected: private Modules only usable by author + grantees (system-wide for all)
- [ ] Tests: template-driven conversation shape, trigger sourcing from template, authorization denials
