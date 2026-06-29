---
name: telegram
description: "Bidirectional Telegram messaging via Bot API — send messages, poll for updates, reply via headless agent."
version: 189
tags: [telegram, bot, messaging]
scope: user
auto_active: true
---

## Purpose

Bidirectional Telegram messaging via Bot API — send messages, poll for updates, reply via headless agent.

In-band slash commands (case-insensitive, processed before LLM invocation):

| Command | Effect |
|---|---|
| `/sessions` | List all registered sessions, active marked with `(*)` |
| `/use <name>` | Switch to session `<name>` (creates it with empty session_id if new) |
| `/use <name> <session_id>` | Switch and explicitly set a session_id (resume existing opencode session) |
| `/status` | Show active session's name, session_id, last_used_at |
| `/model` | Show active model (session-specific or global default) |
| `/model <provider/model>` | Set model for active session (e.g. `deepseek/deepseek-v4-pro`) |
| `/model default` | Clear session-specific model (alias: `clear`, `reset`) → falls back to global |
| `/models` | List configured `provider/model` entries from `~/.config/opencode/opencode.json` |

**Active session** is tracked in `cap_telegram__sessions` (column `is_default=1`). Falls back to legacy `reply_session` config key when the sessions table is empty (backwards compatibility).

**Model resolution** per reply: active session's `model` column → global `reply_model` config → Bash default `claude-sonnet-4-6`.

Note: the `cap_telegram__sessions` table is created by `yesmem setup` or a one-shot `cap_store {action:"create_table", capability:"telegram", table:"sessions", columns:[...]}` call — the `## Database` SQL block below is NOT executed automatically by the CapsDirWatcher sync (Learning #55757).

## Scripts

### telegram_send
kind: tool
schema: {"type":"object","properties":{"text":{"type":"string","description":"Message text"},"chat_id":{"type":"string","description":"Optional chat id override"},"reply_to":{"type":"integer","description":"Optional reply-to message id"}},"required":["text"]}

```bash
exec 2>>/tmp/tsend.log
TOKEN=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["bot_token"],"limit":1}' | yesmem json -r '.rows[0].value')
CHAT_ID=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["chat_id"],"limit":1}' | yesmem json -r '.rows[0].value')
curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" -d "text=${TEXT}" -d "parse_mode=Markdown"
```

### telegram_poll
kind: handler

```bash
exec 2>>/tmp/tpoll.log
printf '[%s] poll start\n' "$(date -Is)" >> /tmp/tpoll.log

TOKEN=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["bot_token"],"limit":1}' | yesmem json -r '.rows[0].value')
if [ -z "$TOKEN" ]; then printf '[%s] poll: no bot_token\n' "$(date -Is)" >> /tmp/tpoll.log; exit 0; fi

OFFSET=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["offset"],"limit":1}' | yesmem json -r '.rows[0].value // "0"')

RESPONSE=$(curl -4 -sS -m 14 "https://api.telegram.org/bot${TOKEN}/getUpdates?offset=${OFFSET}&timeout=12")
RET=$?
if [ $RET -ne 0 ]; then printf '[%s] poll: curl failed exit=%s\n' "$(date -Is)" "$RET" >> /tmp/tpoll.log; exit 0; fi

COUNT=$(echo "$RESPONSE" | yesmem json '.result | length')
if [ -z "$COUNT" ] || [ "$COUNT" = "0" ] || [ "$COUNT" = "null" ]; then
  printf '[%s] poll: no messages\n' "$(date -Is)" >> /tmp/tpoll.log
  exit 0
fi

printf '[%s] poll: %s updates\n' "$(date -Is)" "$COUNT" >> /tmp/tpoll.log
MAX_ID=$OFFSET
N=0
for i in $(seq 0 $((COUNT - 1))); do
  N=$((N + 1))
  if [ $N -gt 50 ]; then printf '[%s] poll: LIMIT 50 reached, deferring remaining\n' "$(date -Is)" >> /tmp/tpoll.log; break; fi
  UPDATE=$(echo "$RESPONSE" | yesmem json ".result[$i]")
  UPD_ID=$(echo "$UPDATE" | yesmem json -r '.update_id')
  PAYLOAD=$(echo "$UPDATE" | yesmem json '{capability:"telegram",action:"upsert",table:"updates",data:{telegram_id:.update_id,chat_id:.message.chat.id,sender:(.message.from.first_name // "unknown"),text:(.message.text // ""),direction:"in",processed:0,date:.message.date}}')
  echo "$PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
  if [ "$UPD_ID" -ge "$MAX_ID" ]; then MAX_ID=$((UPD_ID + 1)); fi
  printf '[%s] poll: stored update_id=%s from %s\n' "$(date -Is)" "$UPD_ID" "$(echo "$UPDATE" | yesmem json -r '(.message.from.first_name // "?")')" >> /tmp/tpoll.log
done

OFFSET_PAYLOAD=$(yesmem json -n --arg key "offset" --arg value "$MAX_ID" '{capability:"telegram",action:"upsert",table:"config",data:{id:12,key:$key,value:$value}}')
echo "$OFFSET_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
printf '[%s] poll: done, offset=%s\n' "$(date -Is)" "$MAX_ID" >> /tmp/tpoll.log
```

### telegram_reply
kind: handler

```bash
exec 2>>/tmp/treply.log
printf '[%s] reply: check\n' "$(date -Is)" >> /tmp/treply.log

CLAIM=$(store '{"capability":"telegram","action":"claim_and_read","table":"updates","where":"processed=0","order":"id ASC","set":{"processed":1},"returning":["id","chat_id","sender","text"]}')
CLAIMED=$(echo "$CLAIM" | yesmem json -r '.claimed')
if [ "$CLAIMED" != "true" ]; then
  printf '[%s] reply: no pending\n' "$(date -Is)" >> /tmp/treply.log
  exit 0
fi

ROW_ID=$(echo "$CLAIM" | yesmem json '.row.id')
TEXT=$(echo "$CLAIM" | yesmem json -r '.row.text')
SENDER=$(echo "$CLAIM" | yesmem json -r '.row.sender')
printf '[%s] reply: replying row=%s sender=%s text=%s\n' "$(date -Is)" "$ROW_ID" "$SENDER" "${TEXT:0:80}" >> /tmp/treply.log

CHAT_ID=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["chat_id"],"limit":1}' | yesmem json -r '.rows[0].value')
TOKEN=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["bot_token"],"limit":1}' | yesmem json -r '.rows[0].value')

# --- Command detection (case-insensitive) ---
NAME=""
UPSERT_PAYLOAD=""
EXPLICIT_SESSION=""
shopt -s nocasematch
if [[ "$TEXT" =~ ^/use[[:space:]]+([A-Za-z0-9_-]{1,32})[[:space:]]+([A-Za-z0-9_-]{1,64})$ ]]; then
  NAME="${BASH_REMATCH[1]}"
  EXPLICIT_SESSION="${BASH_REMATCH[2]}"
  UPSERT_PAYLOAD=$(yesmem json -n --arg name "$NAME" --arg sid "$EXPLICIT_SESSION" '{capability:"telegram","action":"upsert","table":"sessions","data":{name:$name,session_id:$sid,is_default:1}}')
elif [[ "$TEXT" =~ ^/use[[:space:]]+([A-Za-z0-9_-]{1,32})$ ]]; then
  NAME="${BASH_REMATCH[1]}"
  EXISTS_PAYLOAD=$(yesmem json -n --arg name "$NAME" '{capability:"telegram","action":"query","table":"sessions","where":"name=?","args":[$name],"limit":1}')
  EXISTS_COUNT=$(store "$EXISTS_PAYLOAD" | yesmem json '.rows | length')
  # Partial-merge upsert: for existing rows, supply only {name, is_default} — session_id and last_used_at are preserved (cap_store upsert only updates fields in data dict).
  if [ "$EXISTS_COUNT" = "1" ]; then
    UPSERT_PAYLOAD=$(yesmem json -n --arg name "$NAME" '{capability:"telegram","action":"upsert","table":"sessions","data":{name:$name,is_default:1}}')
  else
    UPSERT_PAYLOAD=$(yesmem json -n --arg name "$NAME" '{capability:"telegram","action":"upsert","table":"sessions","data":{name:$name,session_id:"",is_default:1}}')
  fi
elif [[ "$TEXT" =~ ^/sessions?$ ]]; then
  ROWS=$(store '{"capability":"telegram","action":"query","table":"sessions","order":"is_default DESC, name ASC"}')
  COUNT=$(echo "$ROWS" | yesmem json '.rows | length')
  if [ -z "$COUNT" ] || [ "$COUNT" = "0" ] || [ "$COUNT" = "null" ]; then
    MSG="Keine Sessions registriert. /use <name> legt eine an."
  else
    MSG=$(echo "$ROWS" | yesmem json -r '"Sessions (\(.rows | length)):\n" + ([.rows[] | "  \(.name)\(if .is_default == 1 then " (*)" else "") [\(.session_id // "fresh")]"] | join("\n"))')
  fi
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /sessions -> %s\n' "$(date -Is)" "${COUNT:-0}" >> /tmp/treply.log
  exit 0
elif [[ "$TEXT" =~ ^/status$ ]]; then
  STATUS_ROW=$(store '{"capability":"telegram","action":"query","table":"sessions","where":"is_default=1","limit":1}')
  STATUS_NAME=$(echo "$STATUS_ROW" | yesmem json -r '.rows[0].name // empty')
  STATUS_SID=$(echo "$STATUS_ROW" | yesmem json -r '.rows[0].session_id // empty')
  STATUS_USED=$(echo "$STATUS_ROW" | yesmem json -r '.rows[0].last_used_at // empty')
  if [ -z "$STATUS_NAME" ]; then
    MSG="Keine aktive Session. /use <name> schaltet oder legt an."
  else
    MSG="Aktive Session: ${STATUS_NAME}"
    if [ -n "$STATUS_SID" ]; then
      MSG="${MSG} (session: ${STATUS_SID})"
    else
      MSG="${MSG} (neu, startet beim naechsten Reply)"
    fi
    if [ -n "$STATUS_USED" ]; then
      MSG="${MSG}"$'\n'"Zuletzt genutzt: ${STATUS_USED}"
    fi
  fi
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /status -> %s\n' "$(date -Is)" "${STATUS_NAME:-none}" >> /tmp/treply.log
  exit 0
elif [[ "$TEXT" =~ ^/use([[:space:]]|$) ]]; then
  MSG=$'Usage: /use <name> | /use <name> <session_id>\nName: A-Z a-z 0-9 _ - (max 32).'
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /use invalid syntax\n' "$(date -Is)" >> /tmp/treply.log
  exit 0
elif [[ "$TEXT" =~ ^/model$ ]]; then
  ACTIVE_ROW=$(store '{"capability":"telegram","action":"query","table":"sessions","where":"is_default=1","limit":1}')
  ACTIVE_NAME=$(echo "$ACTIVE_ROW" | yesmem json -r '.rows[0].name // empty')
  ACTIVE_MODEL=$(echo "$ACTIVE_ROW" | yesmem json -r '.rows[0].model // empty')
  GLOBAL_MODEL=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["reply_model"],"limit":1}' | yesmem json -r '.rows[0].value // "claude-sonnet-4-6"')
  if [ -z "$ACTIVE_NAME" ]; then
    MSG="Keine aktive Session. /use <name> zuerst, dann /model."
  elif [ -n "$ACTIVE_MODEL" ]; then
    MSG="Aktive Session: ${ACTIVE_NAME}\nModel: ${ACTIVE_MODEL} (session-spezifisch)\nGlobaler Default: ${GLOBAL_MODEL}"
  else
    MSG="Aktive Session: ${ACTIVE_NAME}\nModel: ${GLOBAL_MODEL} (globaler Default)\n/session-spezifisch setzen via: /model <name>"
  fi
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /status model -> %s\n' "$(date -Is)" "${ACTIVE_NAME:-none}" >> /tmp/treply.log
  exit 0
elif [[ "$TEXT" =~ ^/model[[:space:]]+([A-Za-z0-9_./-]{1,64})$ ]]; then
  MODEL_ARG="${BASH_REMATCH[1]}"
  ACTIVE_ROW=$(store '{"capability":"telegram","action":"query","table":"sessions","where":"is_default=1","limit":1}')
  ACTIVE_NAME=$(echo "$ACTIVE_ROW" | yesmem json -r '.rows[0].name // empty')
  if [ -z "$ACTIVE_NAME" ]; then
    MSG="Keine aktive Session. /use <name> zuerst."
    curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
    printf '[%s] reply: /model failed: no active session\n' "$(date -Is)" >> /tmp/treply.log
    exit 0
  fi
  if [[ "$MODEL_ARG" =~ ^(default|clear|reset)$ ]]; then
    CLEAR_PAYLOAD=$(yesmem json -n --arg name "$ACTIVE_NAME" '{capability:"telegram","action":"upsert","table":"sessions","data":{name:$name,model:null}}')
    echo "$CLEAR_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
    GLOBAL_MODEL=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["reply_model"],"limit":1}' | yesmem json -r '.rows[0].value // "claude-sonnet-4-6"')
    MSG="Session ${ACTIVE_NAME}: model cleared → global default (${GLOBAL_MODEL})"
  else
    SET_PAYLOAD=$(yesmem json -n --arg name "$ACTIVE_NAME" --arg model "$MODEL_ARG" '{capability:"telegram","action":"upsert","table":"sessions","data":{name:$name,model:$model}}')
    echo "$SET_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
    MSG="Session ${ACTIVE_NAME}: model gesetzt auf ${MODEL_ARG}"
  fi
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /model -> %s=%s\n' "$(date -Is)" "$ACTIVE_NAME" "$MODEL_ARG" >> /tmp/treply.log
  exit 0
elif [[ "$TEXT" =~ ^/model([[:space:]]|$) ]]; then
  MSG=$'Usage: /model | /model <name> | /model default\nName: A-Z a-z 0-9 _ . / - (max 64).'
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /model invalid syntax\n' "$(date -Is)" >> /tmp/treply.log
  exit 0
elif [[ "$TEXT" =~ ^/models$ ]]; then
  OC_CONFIG="${HOME}/.config/opencode/opencode.json"
  if [ ! -f "$OC_CONFIG" ]; then
    MSG="opencode.json nicht gefunden: $OC_CONFIG"
  else
    MSG=$(yesmem json -r < "$OC_CONFIG" '
      [.provider // {} | to_entries[] | .key as $p |
        (.value.models // {}) | keys[]? | "\($p)/\(.)"
      ] as $explicit |
      [.provider // {} | to_entries[] | select((.value.models // {}) == {}) | "\(.key)/* (default)"] as $default
      | ($explicit + $default) | sort | unique | .[]
    ' 2>&1)
  fi
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /models\n' "$(date -Is)" >> /tmp/treply.log
  exit 0
fi
shopt -u nocasematch

# --- /use switch execution (shared by both /use branches) ---
if [ -n "$NAME" ] && [ -n "$UPSERT_PAYLOAD" ]; then
  # Reorder: set new default FIRST, then reset others. Avoids window with no is_default=1 row.
  echo "$UPSERT_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
  # Reset is_default on every OTHER row that currently has is_default=1.
  # Bug fix: previous version wrapped in [] producing a single JSON array line;
  # the `while read` loop processed only that one line, store failed silently,
  # and stale is_default=1 rows survived. Now jq emits one object per line.
  OTHERS=$(store '{"capability":"telegram","action":"query","table":"sessions","where":"is_default=1","order":"id ASC"}')
  echo "$OTHERS" | yesmem json -r --arg NAME "$NAME" '.rows[] | select(.name != $NAME) | {capability:"telegram",action:"upsert",table:"sessions",data:{name:.name,is_default:0}}' | while IFS= read -r p; do store "$p" > /dev/null; done
  MSG="Aktive Session: ${NAME}"
  if [ -n "$EXPLICIT_SESSION" ]; then MSG="${MSG}"$'\n'"Session: ${EXPLICIT_SESSION}"; fi
  curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${MSG}" > /dev/null
  printf '[%s] reply: /use -> %s\n' "$(date -Is)" "$NAME" >> /tmp/treply.log
  exit 0
fi

# --- Normal reply path ---

# Ensure sessions table exists (auto-create if missing — robustness against pre-setup installs).
# Probe via direct query instead of list_tables (which breaks if cap_store_meta has orphan rows
# referencing dropped tables — see Learning #80227). Query returns a clean "does not exist" error
# string when the table is missing, which we detect explicitly.
SESS_PROBE=$(store '{"capability":"telegram","action":"query","table":"sessions","limit":1}' 2>&1)
if echo "$SESS_PROBE" | grep -q "does not exist"; then
  store '{"capability":"telegram","action":"create_table","table":"sessions","columns":[{"name":"name","type":"TEXT","unique":true},{"name":"session_id","type":"TEXT"},{"name":"is_default","type":"INTEGER"},{"name":"model","type":"TEXT"},{"name":"last_used_at","type":"TEXT"}]}' > /dev/null 2>&1
  printf '[%s] reply: auto-created sessions table\n' "$(date -Is)" >> /tmp/treply.log
fi

# Lazy migration: seed sessions table from legacy reply_session config if empty (one-shot)
SESS_COUNT=$(store '{"capability":"telegram","action":"query","table":"sessions","limit":1}' | yesmem json '.rows | length')
if [ -z "$SESS_COUNT" ] || [ "$SESS_COUNT" = "0" ] || [ "$SESS_COUNT" = "null" ]; then
  LEGACY_SESSION=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["reply_session"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
  if [ -n "$LEGACY_SESSION" ]; then
    SEED=$(yesmem json -n --arg name "default" --arg sid "$LEGACY_SESSION" '{capability:"telegram","action":"upsert","table":"sessions","data":{name:$name,session_id:$sid,is_default:1}}')
    echo "$SEED" | while IFS= read -r p; do store "$p" > /dev/null; done
    # Clear legacy reply_session so re-seed cannot fire after manual row deletion
    CLEAR=$(yesmem json -n '{capability:"telegram","action":"upsert","table":"config","data":{key:"reply_session",value:""}}')
    echo "$CLEAR" | while IFS= read -r p; do store "$p" > /dev/null; done
    printf '[%s] reply: seeded default session from reply_session config (legacy cleared)\n' "$(date -Is)" >> /tmp/treply.log
  fi
fi

# Read active session_id (sessions table preferred, fallback to reply_session config)
SESSION_ID=$(store '{"capability":"telegram","action":"query","table":"sessions","where":"is_default=1","limit":1}' | yesmem json -r '.rows[0].session_id // empty')
if [ -z "$SESSION_ID" ]; then
  SESSION_ID=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["reply_session"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
fi

MODEL=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["reply_model"],"limit":1}' | yesmem json -r '.rows[0].value // "claude-sonnet-4-6"')
# Per-session model overrides global reply_model when set
SESSION_MODEL=$(store '{"capability":"telegram","action":"query","table":"sessions","where":"is_default=1","limit":1}' | yesmem json -r '.rows[0].model // empty')
if [ -n "$SESSION_MODEL" ]; then
  MODEL="$SESSION_MODEL"
fi
SYSPROMPT=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["system_prompt"],"limit":1}' | yesmem json -r '.rows[0].value // "Du bist ein hilfreicher Assistent."')

RESULT=$(llm "$MODEL" "$SYSPROMPT" "Nachricht von $SENDER: $TEXT" "$SESSION_ID" "tools")
LLM_EXIT=$?
if [ "$LLM_EXIT" -ne 0 ]; then
  printf '[%s] reply: llm failed exit=%s\n' "$(date -Is)" "$LLM_EXIT" >> /tmp/treply.log
  exit 0
fi

if ! echo "$RESULT" | yesmem json -e '.' > /dev/null 2>&1; then
  printf '[%s] reply: invalid llm JSON\n' "$(date -Is)" >> /tmp/treply.log
  exit 0
fi

REPLY=$(echo "$RESULT" | yesmem json -r '.result // empty')
REPLY=$(echo "$REPLY" | sed '/^\[[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\} [0-9]\{2\}:[0-9]\{2\}:[0-9]\{2\}\] \[msg:[0-9]\+\]/d')
if [ -z "$REPLY" ]; then
  printf '[%s] reply: empty reply from llm\n' "$(date -Is)" >> /tmp/treply.log
  exit 0
fi

NEW_SESSION=$(echo "$RESULT" | yesmem json -r '.session_id // empty')
if [ -n "$NEW_SESSION" ]; then
  ACTIVE_NAME=$(store '{"capability":"telegram","action":"query","table":"sessions","where":"is_default=1","limit":1}' | yesmem json -r '.rows[0].name // empty')
  if [ -n "$ACTIVE_NAME" ]; then
    NOW=$(date -Is)
    WB=$(yesmem json -n --arg name "$ACTIVE_NAME" --arg sid "$NEW_SESSION" --arg ts "$NOW" '{capability:"telegram","action":"upsert","table":"sessions","data":{name:$name,session_id:$sid,is_default:1,last_used_at:$ts}}')
    echo "$WB" | while IFS= read -r p; do store "$p" > /dev/null; done
  else
    SP=$(yesmem json -n --arg key "reply_session" --arg value "$NEW_SESSION" '{capability:"telegram","action":"upsert","table":"config","data":{key:$key,value:$value}}')
    echo "$SP" | while IFS= read -r p; do store "$p" > /dev/null; done
  fi
fi

SEND=$(curl -4 -s -m 10 "https://api.telegram.org/bot${TOKEN}/sendMessage" -d "chat_id=${CHAT_ID}" --data-urlencode "text=${REPLY}")
CURL_EXIT=$?
if [ "$CURL_EXIT" -ne 0 ] || [ "$(echo "$SEND" | yesmem json -r '.ok // false')" != "true" ]; then
  printf '[%s] reply: sendMessage failed\n' "$(date -Is)" >> /tmp/treply.log
  exit 0
fi
printf '[%s] reply: sent row=%s > %s\n' "$(date -Is)" "$ROW_ID" "${REPLY:0:60}" >> /tmp/treply.log
```

## Database

```sql
CREATE TABLE IF NOT EXISTS cap_telegram__config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    value TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS cap_telegram__conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    sender TEXT NOT NULL,
    role TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS cap_telegram__updates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_id INTEGER UNIQUE,
    chat_id INTEGER,
    sender TEXT,
    text TEXT,
    direction TEXT,
    processed INTEGER NOT NULL DEFAULT 0,
    date INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS cap_telegram__reply_errors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    row_id INTEGER,
    sender TEXT,
    message_text TEXT,
    stage TEXT,
    exit_code INTEGER,
    stderr TEXT,
    stdout TEXT,
    model TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS cap_telegram__sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    session_id TEXT,
    is_default INTEGER NOT NULL DEFAULT 0,
    model TEXT,
    last_used_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```
