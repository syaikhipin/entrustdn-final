# 08: Module registry

**What to build:** Module Authors upload versioned Modules — manifest + markdown and/or config — of exactly one kind (Agent Skill, Process Template, or Connector). Modules are private to the author by default; authors grant/revoke access; Platform Admins review content and promote Modules to system-wide. The manifest documents an A2A capability description (shown, not run). Demoable: upload a Process Template, grant access to another user, admin promotes it.

**Blocked by:** 02 (Membership, Terms of Service & roles).

**Status:** ready-for-agent

- [ ] Module upload with manifest (kind, version, A2A capability description); validation rejects malformed manifests
- [ ] Exactly three kinds enforced; no executable content accepted
- [ ] Private by default; grant/revoke access; system-wide promotion by admin after review
- [ ] Version history retained; a version can be deprecated by author/admin
- [ ] Table-driven tests: upload/validation, visibility rules, promotion, versioning
