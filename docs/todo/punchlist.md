# Punchlist — 2026-04-21

Captured from weave session c79aee5d at 06:23:45Z. These are things dmotles noticed and wants fixed. Ordering is as originally dictated, not by priority.

## 1. Messaging + notification overhaul

Agents should use MCP for send + status reporting. The go harness queues notifications and injects them into the stream when the receiving agent yields — **do not use tmux send-keys**, it collides with active user typing and AskUserQuestion modals.

Two message classes:
- **async**: sub-agent → parent status. Parent reads on its next yield. No interruption.
- **interrupt**: parent → child 'hey I forgot to tell you something, it's important'. Pauses the child, it reads the message, then resumes what it was doing (unless the message explicitly tells it to stop).

## 2. Sub-agent visibility + status reporting

- When I try to see what a sub-agent is doing, I can't see anything.
- Status reporting appears broken both via MCP→parent and in the TUI.

## 3. TUI UX pass

- **Agent switching**: keybinding to iterate through agents, up/down nav on the right-side list, AND a command palette entry like `/switch <agent>` with fuzzy match on partial name.
- **Mouse scroll**: enable mouse support so scrolling over a window just works. Current scrolling is ass.
- **Text selection → clipboard**: highlight should auto-copy to the OS paste buffer, including large selections, and the payload should be raw markdown.

## 4. `sprawl enter` root-loop / Ctrl+C behavior

Why is `sprawl enter` running its own root-loop? Weird things happen on Ctrl+C. Observed:

```
sprawl enter
SPRAWL_ROOT not set — defaulting to /home/coder/qumulo-cloud-sizer-frontend
[enter] starting session 01d13d99-e1d4-458e-9cc0-b9441f709e4e
[root-loop] handoff signal detected, restarting
Stopping agent finn...
Killed agent "finn"
  finn -> stopped
TUI session ended.
```

**Status:** landed as 8bf300f on main (recovered from the 2026-04-21 repo wipe). Verify behavior against the reported symptoms.

## 5. Session doesn't persist across Ctrl+C

After Ctrl+C, running `sprawl enter` again starts fresh instead of resuming the prior session. Expected: resume-by-default (QUM-255).

**Status:** same recovery commit as #4 (8bf300f). Verify.

## 6. Tmux-mode "consolidating timeline" is slow

Takes forever. Whatever it's doing will almost certainly bite TUI mode too once that path is wired up.

## 7. "Updating persistent knowledge" — what is it doing?

Same deal — slow and opaque. Need visibility into what this step actually does and why it takes the time it does.

## 8. `sprawl spawn subagent` is broken

Reported 2026-04-21. The subagent spawn path is broken — docs/system prompt describe it as a lightweight agent sharing the parent's worktree, but the CLI rejects with 'required flag(s) "branch" not set'. Either fix the command to live up to its description or update docs to match actual behavior.

---

**Note on tracking:** Linear was over quota when this was captured, and we later decided to abandon the GitHub migration too — it wasn't worth the pain right now. For now this file is the source of truth until a tracking decision is made.
