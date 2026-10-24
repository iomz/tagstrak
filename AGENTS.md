# Agent instructions

## Commit attribution

A commit materially authored by an AI tool ends with exactly one trailer: a `Co-authored-by` line naming that tool, in the format the tool specifies.
Examples from this repository's history:

```text
Co-authored-by: Codex <noreply@openai.com>
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
```

Do not add session, conversation, or link trailers, or any other generated trailer.
Do not add a co-author trailer to commits the maintainer wrote, even when a tool committed them; ask about attribution for uncommitted work whose authorship is unclear.
