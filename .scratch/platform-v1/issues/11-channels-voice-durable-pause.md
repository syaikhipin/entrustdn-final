# 11: Channels, voice & durable pause

**What to build:** The Agent converses with Farmer Members beyond web chat: WhatsApp and Telegram Channel adapters (fakes in tests), email Channel (dev log sink), and voice notes transcribed via STT (env-keyed, agent-side; fake STT in tests) with text replies. Conversations pause for days awaiting a Member's reply and resume mid-thread from LangGraph checkpoints (the ADR 0001 core scenario). The Farmer Organization maintains a Member roster (contact points, no Member accounts), Members receive resumable links after partial answers, and over-survey protection stops questions when quality triggers are met. Demoable: chat with a fake channel, kill the agent process, restart, reply days-later-style, conversation resumes.

**Blocked by:** 07 (Requests & agent clarification over web chat).

**Status:** ready-for-agent

- [ ] Channel adapter interface with WhatsApp, Telegram, email implementations + fakes (Seam 3)
- [ ] Voice notes in → STT transcription → text reply; STT client env-keyed, faked in tests
- [ ] Durable pause: conversation survives agent restart and resumes mid-thread on Member reply (checkpointed state)
- [ ] Org Member roster: contact points with channel addresses, no accounts
- [ ] Resumable member links after partial answers
- [ ] Integration test proves pause-for-days/resume with real backend+agent processes (the Seam 2 thin suite)
