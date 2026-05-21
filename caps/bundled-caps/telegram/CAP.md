---
name: telegram
description: "Bidirectional Telegram messaging via Bot API — send messages, poll for updates, reply via headless agent."
version: 168
tags: [telegram, bot, messaging]
scope: user
auto_active: true
---

## Purpose

Bidirectional Telegram messaging via Bot API — send messages, poll for updates, reply via headless agent.

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
MODEL=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["reply_model"],"limit":1}' | yesmem json -r '.rows[0].value // "claude-sonnet-4-6"')
SYSPROMPT=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["system_prompt"],"limit":1}' | yesmem json -r '.rows[0].value // "Du bist ein hilfreicher Assistent."')
SESSION_ID=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["reply_session"],"limit":1}' | yesmem json -r '.rows[0].value // empty')

TOKEN=$(store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["bot_token"],"limit":1}' | yesmem json -r '.rows[0].value')
RESULT=$(llm "$MODEL" "$SYSPROMPT" "Nachricht von $SENDER: $TEXT" "$SESSION_ID")
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
  SP=$(yesmem json -n --arg key "reply_session" --arg value "$NEW_SESSION" '{capability:"telegram",action:"upsert",table:"config",data:{key:$key,value:$value}}')
  echo "$SP" | while IFS= read -r p; do store "$p" > /dev/null; done
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
    value TEXT NOT NULL
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
```
