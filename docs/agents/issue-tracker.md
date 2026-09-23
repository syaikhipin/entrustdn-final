# Issue tracker: GitHub Issues

Issues and specs for this repo live on GitHub: `syaikhipin/entrustdn-final`
(issues migrated from the former local markdown tracker on 2026-09-22).

## Conventions

- One epic per feature; all platform-v1 tickets carry the `platform-v1` label.
- Ticket number NN maps to GitHub issue #NN (they were migrated in order).
- Open/closed is triage state: `resolved` tickets are closed with reason
  "completed"; work in flight stays open.
- Comments and conversation history are issue comments.
- `Blocked by: #NN` lines in ticket bodies express dependencies.

## When a skill says "publish to the issue tracker"

Create a GitHub issue (`gh issue create --label platform-v1`).

## When a skill says "fetch the relevant ticket"

`gh issue view <number>` — the user will normally pass the number directly.

## Wayfinding operations

Used by `/wayfinder`, translated to GitHub:

- **Map**: the epic's tracking issue (or `.scratch/<effort>/map.md` while it
  holds Notes / Decisions-so-far / Fog).
- **Child ticket**: one GitHub issue per question/task, numbered by GitHub.
- **Blocking**: `Blocked by: #NN, #NN` in the body. A ticket is unblocked
  when every issue it lists is closed.
- **Frontier**: `gh issue list --label platform-v1 --state open`, filtered
  for unblocked bodies; lowest number wins.
- **Claim**: assign yourself (`gh issue edit <n> --add-assignee @me`) before
  any work.
- **Resolve**: post the answer as an issue comment, close with reason
  "completed", and append a context pointer to the map's Decisions-so-far.
