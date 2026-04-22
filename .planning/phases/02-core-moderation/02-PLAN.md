# Phase 2: Core Moderation & UI - Implementation Plan

## Objective
Implement auto-deletion of messages matching regex rules and add management commands (`/addRule`, `/delRule`, `/list`).

## Requirements Covered
- [FEAT-001]: Message Monitoring.
- [FEAT-002]: Keyword/Regex Deletion.
- [FEAT-003]: Moderation Commands (`/addRule`, `/delRule`, `/list` integration).

## Plan Steps

### Step 1: Config Updates
**File**: `config.go`
1. Add `Rules []string` to the `Conf` struct to persist regex string configurations.

### Step 2: Global State & Regex Compilation
**File**: `main.go`
1. Add `RegexRules []*regexp.Regexp` to the `Infos` struct.
2. Create a new function `(infos *Infos) buildRegex()` that clears `RegexRules` and compiles all valid strings in `infos.Conf.Rules` into `RegexRules`. Invalid regex strings should be skipped and logged.
3. In `newInfos()`, call `infos.buildRegex()` immediately after `loadConf()` and `buildIDs()`.

### Step 3: Message Interception
**File**: `command.go`
1. In `handleBotCommand`, immediately after checking if the message is from the bot itself:
   - Check if `!strings.HasPrefix(text, "/")`.
   - If it's a regular text message, iterate through `infos.RegexRules`.
   - If `r.MatchString(text)` is true, call `m.Delete()`. Then `return nil` to stop processing.

### Step 4: Management Commands
**File**: `command.go`
1. **Extend `/list` command**:
   - In the `/list` case, append the currently active Rules from `infos.Conf.Rules` to the output message so the user can see them alongside channels and whitelists.
2. **Add `/addRule` command**:
   - Verify `infos.isAdmin()`.
   - Extract the regex rule from the text.
   - Verify it compiles using `regexp.Compile`.
   - Append to `infos.Conf.Rules`, call `infos.buildRegex()`, save using `saveConf()`, and return a success message.
3. **Add `/delRule` command**:
   - Verify `infos.isAdmin()`.
   - Parse the argument as an index (1-based or 0-based, depending on convention, typically 0-based internally but 1-based for users is better) OR match by exact string.
   - Remove the rule from `infos.Conf.Rules`, call `infos.buildRegex()`, save using `saveConf()`, and return a success message.

## Dependencies
- Must have `delete_messages` permission in the group. This is handled by Telegram, our code just calls `m.Delete()`.

## Verification Strategy
- Send a normal message containing a defined keyword in a group where the bot is admin. It should be deleted.
- Add and remove rules to ensure persistence works.
