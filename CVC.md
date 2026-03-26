# CVC (Chat Version Control)

CVC lets you snapshot and restore the current conversation state.

## Commands

- `/commit <message>`: Create a commit from the current chat history.
- `/commit-search <query>`: Search commits by ref prefix or message text. Results are capped at 5.
- `/commit-pull <ref>`: Restore a commit by ref (supports prefix matching and tab selection).
- `/commit-merge <ref>`: Append a commit’s messages to the current conversation state.

## What Gets Saved

Each commit stores the full chat transcript (user + assistant turns) at the moment you run `/commit`.

## Where Commits Live

Commit files are stored under:

```
$HOME/.godex/commits/<sha256(normalized_wd)>/<commit_ref>
```

The `commits` table in the SQLite history DB stores:

- `wd` (working directory)
- `ref` (commit hash)
- `message`
- `created_at`

## Restore Behavior

When you restore a commit:

1. The provider context is reset.
2. The provider’s message history is replaced with the committed transcript.

This fully replaces the current conversation state.

When you merge a commit:

1. The current conversation state is preserved.
2. The commit’s messages are appended to the provider’s message history.

## UI Details

- Lists show up to 5 commits.
- Each result displays the commit message (truncated to 129 chars), date, and ref.
- In search results, the ref is shown as muted subtext.
