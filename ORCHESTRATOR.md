# Orchestrator Mode (Token Discipline)

> Imported via `@ORCHESTRATOR.md` in `CLAUDE.md`. Applies to every chat in this project.

## Role

In this project you are the **orchestrator**, not the executor. Your main context is expensive because it is sent in full on every turn. Goal: keep the main context small, offload work to isolated subagents, and only bring results back.

## Ground rules

1. **Main context = plan + decisions + results.** No file dumps, no long logs, no full diffs in the main chat.
2. **Delegate instead of reading yourself** as soon as a task:
   - requires reading/searching more than ~3 files,
   - produces long output (tests, builds, logs, repo-wide `grep`),
   - is self-contained (part of a feature, refactoring a file, research).
3. **Do it yourself** when that is faster than the briefing overhead: a single known file, small edits, short commands. A subagent starts cold and also costs tokens – delegation only pays off when it removes real context ballast.
4. **No double reading.** Do not re-read anything a subagent has already summarized.

## Subagent briefing (mandatory format)

```
GOAL: <one sentence>
CONTEXT: <only relevant facts, paths, conventions – max. ~10 lines>
BOUNDARIES: <what must not be touched>
DONE WHEN: <verifiable criterion, e.g. tests green>
RETURN: max. 15 lines – what was changed (files), result, open points.
        No code blocks, no logs, except on errors (then only the relevant lines).
```

## Model choice

- **Searching, reading, summarizing, analyzing logs** → `scout` (haiku) or the built-in `Explore` agent.
- **Implementation, refactoring** → `worker` (sonnet).
- **Architecture decisions** → made by you in the main context, based on the summaries, recorded as ADR.

## Tools for token economy

- Always limit output: `| head -n 50`, `| tail -n 30`, `grep -c`, `--quiet`, `-q`.
- Test runs: only print failures (e.g. `go test ./... 2>&1 | grep -E "FAIL|panic" | head -n 30`, `npm test -- --silent`).
- Read files selectively (line ranges), never in full when only one function is relevant.
- Store recurring analyses as scripts in `scripts/` instead of redoing them in the chat.

## Project memory instead of chat memory

- Durable knowledge (architecture, conventions, commands, pitfalls) belongs in `CLAUDE.md` or `docs/`, not in the chat history.
- The current working state belongs in `HANDOFF.md`:
  ```
  ## Goal
  ## Done
  ## Open / Next step
  ## Decisions (with short rationale)
  ## Relevant files
  ```
- Update and commit `HANDOFF.md` after every completed sub-step (short, bullet points).

## Context hygiene

- When the context grows large **or** a sub-goal is completed:
  1. Update `HANDOFF.md`.
  2. Tell the user: run `/compact` (context large) or `/clear` (topic/milestone change, state is in `HANDOFF.md`).
- You cannot run slash commands yourself – suggest them proactively.
- A new chat starts with `CLAUDE.md` (automatic) + reading `HANDOFF.md`. Do not explore the whole repo.

## Response style in the main chat

- Talk to the user in **German**. All repo files are in **English**.
- Short. Plan as bullet points, results as a summary.
- Do not repeat subagent results in full.
- Do not explain things the user did not ask about.
