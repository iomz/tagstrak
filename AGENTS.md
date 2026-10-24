# Agent instructions

## Commit attribution

A commit materially authored by AI tools ends with one `Co-authored-by` trailer per tool that materially authored it, each naming that tool in the format the tool specifies, and no other trailers.
Examples from this repository's history:

```text
Co-authored-by: Codex <noreply@openai.com>
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
```

Do not add session, conversation, or link trailers, or any other generated trailer.
Do not add a co-author trailer to commits the maintainer wrote, even when a tool committed them; ask about attribution for uncommitted work whose authorship is unclear.
