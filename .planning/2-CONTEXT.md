# Phase 2: Core Moderation & Phase 3: Moderation UI - Context

## Decisions

### 1. Rule Storage (Persistence)
- **Decision**: Add a `Rules []string` slice to the `Conf` struct in `config.go`.
- **Reason**: This allows rules to be naturally saved to and loaded from `config.json` using the existing `loadConf` and `saveConf` functions.

### 2. Performance Optimization
- **Decision**: Add a `RegexRules []*regexp.Regexp` cache to the `Infos` struct.
- **Reason**: Compiling regular expressions on every incoming message is expensive. We will compile them once during startup (or when rules are updated via commands) and use the compiled cache for matching.

### 3. Message Interception
- **Decision**: Extend `handleBotCommand` in `command.go`.
- **Reason**: It is the existing `telegram.OnMessage` handler. We will add a check: if the message text matches any rule in `RegexRules`, call `m.Delete()` and return early.

### 4. Moderation UI (Phase 3 Integration)
- **Decision**: Add `/addRule`, `/delRule` to the switch block in `handleBotCommand`. Extend the existing `/list` command to also print `infos.Conf.Rules`.

## Canonical References
- `f:\文档\Go\TGBot\config.go`
- `f:\文档\Go\TGBot\command.go`
- `f:\文档\Go\TGBot\main.go`
