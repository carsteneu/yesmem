---
name: rocketchat
description: "Bidirectional Rocket.Chat messaging via REST API — send messages, poll channel history, reply via headless agent. Commands use /yesmem prefix."
version: 11
tags: [rocketchat, bot, messaging, chat]
scope: user
tested: false
auto_active: true
---

## Purpose

Bidirectional Rocket.Chat messaging via REST API (self-hosted, e.g. https://chat.papoo-service.de) — mirror of the telegram cap: send messages, poll a private channel (`chief`) for inbound messages, reply via headless agent with session handling.

Transport decisions (documented per task contract):
- REST polling only (login → cached auth token → `groups.history` with `oldest` cursor). No WebSocket/DDP daemon — cap-first, no new service infrastructure.
- Auth: `POST /api/v1/login` with user/password from config; `authToken`/`userId` cached in config (`rc_auth_token`, `rc_user_id`), refreshed on HTTP 401. Personal access tokens are a possible later optimization — token caching is sufficient.
- Secrets (password, auth token) are never passed as curl argv: headers go via `-H @file` (0600, deleted after use), bodies via stdin (`-d @-`). See gotchas #83571/#83632.
- Commands are prefixed `/yesmem …` (`/yesmem use`, `/yesmem sessions`, `/yesmem status`, `/yesmem model`, `/yesmem models`) so they cannot collide with Rocket.Chat's own slash commands (user decision 2026-08-18). The RC client intercepts unknown slash commands locally — typing `\yesmem status` (backslash escape) sends the command as plain text; the reply handler normalizes `\yesmem` → `/yesmem`.
- The bot's own messages are filtered in poll (`u.username == rc_user`, case-insensitive) to prevent reply loops.
- Backlog: poll fetches count=50 per run; a >50-message burst between polls defers the excess to subsequent runs (documented design limit, mirrors the telegram cap).
- First poll run seeds the cursor to the newest message timestamp without processing backlog.
- Poll only stores; reply is a separate handler (claim_and_read) — same separation as telegram.
- rc_api runs inside `$(…)` subshells, so HTTP code and body are both printed to stdout; callers split them via RC_RESP/RC_HTTP_CODE helper functions (plain assignment, no subshell).

## Scripts

### rocketchat_send
kind: tool
schema: {"type":"object","properties":{"text":{"type":"string","description":"Message text"},"rid":{"type":"string","description":"Optional room id override"},"reply_to":{"type":"string","description":"Optional thread parent message id (tmid)"}},"required":["text"]}

```bash
umask 077
exec 2>>/tmp/rcsend.log

# Decode Go json.Marshal HTML escapes from injected args (\u003c etc. arrive as literal text)
# and literal \n sequences (daemon arg injection cannot carry real newlines — they break the script).
TEXT=$(printf '%s' "$TEXT" | sed 's/\\u003[Cc]/</g; s/\\u003[Ee]/>/g; s/\\u0026/\&/g; s/\\n/\n/g')
REPLY_TO=$(printf '%s' "${REPLY_TO:-}" | sed 's/\\u003[Cc]/</g; s/\\u003[Ee]/>/g; s/\\u0026/\&/g')

URL=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_url"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RC_USER=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_user"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RC_PASS=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_pass"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RID="${RID:-$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_rid"],"limit":1}' | yesmem json -r '.rows[0].value // empty')}"
AUTH_TOKEN=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_auth_token"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
AUTH_UID=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_user_id"],"limit":1}' | yesmem json -r '.rows[0].value // empty')

if [ -z "$URL" ] || [ -z "$RC_USER" ] || [ -z "$RC_PASS" ] || [ -z "$RID" ]; then
  printf '[%s] send: missing config\n' "$(date -Is)" >> /tmp/rcsend.log
  echo '{"error":"missing config (rc_url/rc_user/rc_pass/rc_rid)"}'
  exit 0
fi

rc_login() {
  local body resp tok uid
  body=$(yesmem json -n --arg user "$RC_USER" --arg pass "$RC_PASS" '{user:$user,password:$pass}')
  resp=$(printf '%s' "$body" | curl -s -m 8 -H 'Content-Type: application/json' -d @- "$URL/api/v1/login")
  tok=$(echo "$resp" | yesmem json -r '.data.authToken // empty')
  uid=$(echo "$resp" | yesmem json -r '.data.userId // empty')
  if [ -z "$tok" ] || [ -z "$uid" ]; then
    printf '[%s] send: login failed\n' "$(date -Is)" >> /tmp/rcsend.log
    return 1
  fi
  AUTH_TOKEN="$tok"
  AUTH_UID="$uid"
  store "$(yesmem json -n --arg key "rc_auth_token" --arg value "$tok" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
  store "$(yesmem json -n --arg key "rc_user_id" --arg value "$uid" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
}

rc_api() {
  # $1 = path (with query), $2 = JSON body (optional → POST). Prints body + http_code (last line).
  local out
  hdrfile=$(mktemp /tmp/rc_hdr.XXXXXX)
  chmod 600 "$hdrfile"
  {
    printf 'X-Auth-Token: %s\n' "$AUTH_TOKEN"
    printf 'X-User-Id: %s\n' "$AUTH_UID"
  } > "$hdrfile"
  if [ -n "$2" ]; then
    out=$(printf '%s' "$2" | curl -s -m 8 -H @"$hdrfile" -H 'Content-Type: application/json' -d @- -w '\n%{http_code}' "$URL$1")
  else
    out=$(curl -s -m 8 -H @"$hdrfile" -w '\n%{http_code}' "$URL$1")
  fi
  rm -f "$hdrfile"
  printf '%s' "$out"
}
trap 'rm -f "$hdrfile" 2>/dev/null' EXIT

rc_get() {
  local full
  full=$(rc_api "$1")
  RC_HTTP_CODE="${full##*$'\n'}"
  RC_RESP="${full%$'\n'*}"
}

rc_post() {
  local full
  full=$(rc_api "$1" "$2")
  RC_HTTP_CODE="${full##*$'\n'}"
  RC_RESP="${full%$'\n'*}"
}

if [ -z "$AUTH_TOKEN" ] || [ -z "$AUTH_UID" ]; then
  rc_login || { echo '{"error":"login failed"}'; exit 0; }
fi

MSG_JSON=$(yesmem json -n --arg rid "$RID" --arg msg "$TEXT" --arg tmid "${REPLY_TO:-}" 'if $tmid == "" then {message:{rid:$rid,msg:$msg}} else {message:{rid:$rid,msg:$msg,tmid:$tmid}} end')
rc_post "/api/v1/chat.sendMessage" "$MSG_JSON"
if [ "$RC_HTTP_CODE" = "401" ]; then
  rc_login || { echo '{"error":"relogin failed"}'; exit 0; }
  rc_post "/api/v1/chat.sendMessage" "$MSG_JSON"
fi

echo "$RC_RESP"
printf '[%s] send: http=%s ok=%s\n' "$(date -Is)" "${RC_HTTP_CODE:-?}" "$(echo "$RC_RESP" | yesmem json -r '.success // "?"')" >> /tmp/rcsend.log
```

### rocketchat_poll
kind: handler

```bash
umask 077
exec 2>>/tmp/rcpoll.log
printf '[%s] poll start\n' "$(date -Is)" >> /tmp/rcpoll.log

URL=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_url"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RC_USER=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_user"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RC_PASS=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_pass"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RID=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_rid"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
AUTH_TOKEN=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_auth_token"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
AUTH_UID=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_user_id"],"limit":1}' | yesmem json -r '.rows[0].value // empty')

if [ -z "$URL" ] || [ -z "$RC_USER" ] || [ -z "$RC_PASS" ] || [ -z "$RID" ]; then
  printf '[%s] poll: no config\n' "$(date -Is)" >> /tmp/rcpoll.log
  exit 0
fi

rc_login() {
  local body resp tok uid
  body=$(yesmem json -n --arg user "$RC_USER" --arg pass "$RC_PASS" '{user:$user,password:$pass}')
  resp=$(printf '%s' "$body" | curl -s -m 8 -H 'Content-Type: application/json' -d @- "$URL/api/v1/login")
  tok=$(echo "$resp" | yesmem json -r '.data.authToken // empty')
  uid=$(echo "$resp" | yesmem json -r '.data.userId // empty')
  if [ -z "$tok" ] || [ -z "$uid" ]; then
    printf '[%s] poll: login failed\n' "$(date -Is)" >> /tmp/rcpoll.log
    return 1
  fi
  AUTH_TOKEN="$tok"
  AUTH_UID="$uid"
  store "$(yesmem json -n --arg key "rc_auth_token" --arg value "$tok" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
  store "$(yesmem json -n --arg key "rc_user_id" --arg value "$uid" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
}

rc_api() {
  # $1 = path (with query), $2 = JSON body (optional → POST). Prints body + http_code (last line).
  local out
  hdrfile=$(mktemp /tmp/rc_hdr.XXXXXX)
  chmod 600 "$hdrfile"
  {
    printf 'X-Auth-Token: %s\n' "$AUTH_TOKEN"
    printf 'X-User-Id: %s\n' "$AUTH_UID"
  } > "$hdrfile"
  if [ -n "$2" ]; then
    out=$(printf '%s' "$2" | curl -s -m 8 -H @"$hdrfile" -H 'Content-Type: application/json' -d @- -w '\n%{http_code}' "$URL$1")
  else
    out=$(curl -s -m 8 -H @"$hdrfile" -w '\n%{http_code}' "$URL$1")
  fi
  rm -f "$hdrfile"
  printf '%s' "$out"
}
trap 'rm -f "$hdrfile" 2>/dev/null' EXIT

rc_get() {
  local full
  full=$(rc_api "$1")
  RC_HTTP_CODE="${full##*$'\n'}"
  RC_RESP="${full%$'\n'*}"
}

if [ -z "$AUTH_TOKEN" ] || [ -z "$AUTH_UID" ]; then
  rc_login || exit 0
fi

CURSOR=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_latest"],"limit":1}' | yesmem json -r '.rows[0].value // empty')

if [ -z "$CURSOR" ]; then
  rc_get "/api/v1/groups.history?roomId=${RID}&count=1"
  if [ "$RC_HTTP_CODE" = "401" ]; then
    rc_login || exit 0
    rc_get "/api/v1/groups.history?roomId=${RID}&count=1"
  fi
  NEWEST=$(echo "$RC_RESP" | yesmem json -r '.messages[0].ts // empty')
  if [ -n "$NEWEST" ]; then
    store "$(yesmem json -n --arg key "rc_latest" --arg value "$NEWEST" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
    printf '[%s] poll: first run, cursor seeded to %s\n' "$(date -Is)" "$NEWEST" >> /tmp/rcpoll.log
  else
    printf '[%s] poll: first run, no messages\n' "$(date -Is)" >> /tmp/rcpoll.log
  fi
  exit 0
fi

rc_get "/api/v1/groups.history?roomId=${RID}&oldest=${CURSOR}&inclusive=false&count=50"
if [ "$RC_HTTP_CODE" = "401" ]; then
  rc_login || exit 0
  rc_get "/api/v1/groups.history?roomId=${RID}&oldest=${CURSOR}&inclusive=false&count=50"
fi

if [ "$RC_HTTP_CODE" != "200" ]; then
  printf '[%s] poll: http=%s\n' "$(date -Is)" "${RC_HTTP_CODE:-?}" >> /tmp/rcpoll.log
  exit 0
fi

CLEAN=$(echo "$RC_RESP" | yesmem json -r --arg me "$RC_USER" '[.messages[]? | select(((.u.username // "") | ascii_downcase) != ($me | ascii_downcase)) | select(.t == null) | select((.msg // "") != "")] | sort_by(.ts)')
COUNT=$(echo "$CLEAN" | yesmem json 'length')
if [ -z "$COUNT" ] || [ "$COUNT" = "0" ] || [ "$COUNT" = "null" ]; then
  printf '[%s] poll: no messages\n' "$(date -Is)" >> /tmp/rcpoll.log
  exit 0
fi

printf '[%s] poll: %s messages\n' "$(date -Is)" "$COUNT" >> /tmp/rcpoll.log
MAX_TS="$CURSOR"
N=0
for i in $(seq 0 $((COUNT - 1))); do
  N=$((N + 1))
  if [ $N -gt 50 ]; then printf '[%s] poll: LIMIT 50 reached, deferring remaining\n' "$(date -Is)" >> /tmp/rcpoll.log; break; fi
  MSG=$(echo "$CLEAN" | yesmem json ".[$i]")
  PAYLOAD=$(echo "$MSG" | yesmem json '{capability:"rocketchat","action":"upsert",table:"updates",data:{rc_message_id:._id,rid:.rid,sender:(.u.username // "unknown"),text:(.msg // ""),direction:"in",ts:(.ts // "")}}')
  echo "$PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null || printf '[%s] poll: store FAILED for this message\n' "$(date -Is)" >> /tmp/rcpoll.log; done
  TS=$(echo "$MSG" | yesmem json -r '.ts // empty')
  if [ -n "$TS" ] && [[ "$TS" > "$MAX_TS" ]]; then MAX_TS="$TS"; fi
  printf '[%s] poll: stored %s from %s\n' "$(date -Is)" "$(echo "$MSG" | yesmem json -r '._id')" "$(echo "$MSG" | yesmem json -r '(.u.username // "?")')" >> /tmp/rcpoll.log
done

store "$(yesmem json -n --arg key "rc_latest" --arg value "$MAX_TS" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
printf '[%s] poll: done, cursor=%s\n' "$(date -Is)" "$MAX_TS" >> /tmp/rcpoll.log
```

### rocketchat_reply
kind: handler

```bash
umask 077
exec 2>>/tmp/rcreply.log
printf '[%s] reply: check\n' "$(date -Is)" >> /tmp/rcreply.log

CLAIM=$(store '{"capability":"rocketchat","action":"claim_and_read","table":"updates","where":"processed=0","order":"id ASC","set":{"processed":1},"returning":["id","rid","sender","text"]}')
CLAIMED=$(echo "$CLAIM" | yesmem json -r '.claimed')
if [ "$CLAIMED" != "true" ]; then
  printf '[%s] reply: no pending\n' "$(date -Is)" >> /tmp/rcreply.log
  exit 0
fi

ROW_ID=$(echo "$CLAIM" | yesmem json '.row.id')
TEXT=$(echo "$CLAIM" | yesmem json -r '.row.text')
# RC client intercepts unknown slash commands; \ escapes them ("\yesmem ..." arrives as text). Normalize:
TEXT=$(printf '%s' "$TEXT" | sed 's|^\\[Yy][Ee][Ss][Mm][Ee][Mm]|/yesmem|')
SENDER=$(echo "$CLAIM" | yesmem json -r '.row.sender')
printf '[%s] reply: replying row=%s sender=%s text=%s\n' "$(date -Is)" "$ROW_ID" "$SENDER" "${TEXT:0:80}" >> /tmp/rcreply.log

URL=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_url"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RC_USER=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_user"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RC_PASS=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_pass"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
RID=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_rid"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
AUTH_TOKEN=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_auth_token"],"limit":1}' | yesmem json -r '.rows[0].value // empty')
AUTH_UID=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["rc_user_id"],"limit":1}' | yesmem json -r '.rows[0].value // empty')

rc_login() {
  local body resp tok uid
  body=$(yesmem json -n --arg user "$RC_USER" --arg pass "$RC_PASS" '{user:$user,password:$pass}')
  resp=$(printf '%s' "$body" | curl -s -m 15 -H 'Content-Type: application/json' -d @- "$URL/api/v1/login")
  tok=$(echo "$resp" | yesmem json -r '.data.authToken // empty')
  uid=$(echo "$resp" | yesmem json -r '.data.userId // empty')
  if [ -z "$tok" ] || [ -z "$uid" ]; then
    printf '[%s] reply: login failed\n' "$(date -Is)" >> /tmp/rcreply.log
    return 1
  fi
  AUTH_TOKEN="$tok"
  AUTH_UID="$uid"
  store "$(yesmem json -n --arg key "rc_auth_token" --arg value "$tok" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
  store "$(yesmem json -n --arg key "rc_user_id" --arg value "$uid" '{capability:"rocketchat","action":"upsert","table":"config","data":{key:$key,value:$value}}')" > /dev/null
}

rc_api() {
  # $1 = path (with query), $2 = JSON body (optional → POST). Prints body + http_code (last line).
  local out
  hdrfile=$(mktemp /tmp/rc_hdr.XXXXXX)
  chmod 600 "$hdrfile"
  {
    printf 'X-Auth-Token: %s\n' "$AUTH_TOKEN"
    printf 'X-User-Id: %s\n' "$AUTH_UID"
  } > "$hdrfile"
  if [ -n "$2" ]; then
    out=$(printf '%s' "$2" | curl -s -m 15 -H @"$hdrfile" -H 'Content-Type: application/json' -d @- -w '\n%{http_code}' "$URL$1")
  else
    out=$(curl -s -m 15 -H @"$hdrfile" -w '\n%{http_code}' "$URL$1")
  fi
  rm -f "$hdrfile"
  printf '%s' "$out"
}
trap 'rm -f "$hdrfile" 2>/dev/null' EXIT

rc_post() {
  local full
  full=$(rc_api "$1" "$2")
  RC_HTTP_CODE="${full##*$'\n'}"
  RC_RESP="${full%$'\n'*}"
}

rc_send() {
  # Split into RC-safe chunks: chat.sendMessage rejects >5000 UTF-16 units
  # (Message_MaxAllowedSize) with a silent 400. 4800 leaves headroom; splits
  # prefer line boundaries. Log full server body on any failure.
  local chunk MSG_JSON SENT
  SENT=0
  while IFS= read -r -d '' chunk; do
    [ -n "$chunk" ] || continue
    MSG_JSON=$(yesmem json -n </dev/null --arg rid "$RID" --arg msg "$chunk" '{message:{rid:$rid,msg:$msg}}')
    rc_post "/api/v1/chat.sendMessage" "$MSG_JSON"
    if [ "$RC_HTTP_CODE" = "401" ]; then
      rc_login || return 1
      rc_post "/api/v1/chat.sendMessage" "$MSG_JSON"
    fi
    if [ "$RC_HTTP_CODE" != "200" ]; then
      printf '[%s] reply: send FAILED http=%s body=%s\n' "$(date -Is)" "${RC_HTTP_CODE:-?}" "$RC_RESP" >> /tmp/rcreply.log
      ERR_PAYLOAD=$(yesmem json -n </dev/null --arg rid "${ROW_ID:-0}" --arg sender "${SENDER:-}" --arg stage "send" --arg msg "$chunk" --arg code "${RC_HTTP_CODE:-0}" --arg body "$RC_RESP" --arg model "$MODEL" --arg ts "$(date -Is)" '{capability:"rocketchat","action":"upsert","table":"reply_errors","data":{row_id:$rid,sender:$sender,stage:$stage,message_text:$msg,http_code:$code,error_body:$body,model:$model,created_at:$ts}}')
      echo "$ERR_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null 2>&1; done
      return 1
    fi
    SENT=$((SENT+1))
  done < <(printf '%s' "$1" | python3 -c '
import sys
def u16len(s): return len(s.encode("utf-16-le"))//2
LIMIT=4800
text=sys.stdin.read()
chunks=[]; cur=""
def flush():
    global cur
    if cur: chunks.append(cur); cur=""
lines=text.split("\n")
for i,line in enumerate(lines):
    unit=line+("" if i==len(lines)-1 else "\n")
    if u16len(cur)+u16len(unit)<=LIMIT:
        cur+=unit; continue
    flush()
    if u16len(unit)<=LIMIT:
        cur=unit; continue
    buf=""
    for ch in unit:
        if buf and u16len(buf)+u16len(ch)>LIMIT:
            chunks.append(buf); buf=""
        buf+=ch
    cur=buf
flush()
for c in chunks:
    sys.stdout.write(c); sys.stdout.write("\0")
')
  printf '[%s] reply: sent %s chunk(s) ok\n' "$(date -Is)" "$SENT" >> /tmp/rcreply.log
}

# --- Command detection (case-insensitive, /yesmem prefix to avoid RC built-in collisions) ---
NAME=""
UPSERT_PAYLOAD=""
EXPLICIT_SESSION=""
shopt -s nocasematch
if [[ "$TEXT" =~ ^/yesmem[[:space:]]+use[[:space:]]+([A-Za-z0-9_-]{1,32})[[:space:]]+([A-Za-z0-9_-]{1,64})$ ]]; then
  NAME="${BASH_REMATCH[1]}"
  EXPLICIT_SESSION="${BASH_REMATCH[2]}"
  UPSERT_PAYLOAD=$(yesmem json -n --arg name "$NAME" --arg sid "$EXPLICIT_SESSION" '{capability:"rocketchat","action":"upsert","table":"sessions","data":{name:$name,session_id:$sid,is_default:1}}')
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+use[[:space:]]+([A-Za-z0-9_-]{1,32})$ ]]; then
  NAME="${BASH_REMATCH[1]}"
  EXISTS_PAYLOAD=$(yesmem json -n --arg name "$NAME" '{capability:"rocketchat","action":"query","table":"sessions","where":"name=?","args":[$name],"limit":1}')
  EXISTS_COUNT=$(store "$EXISTS_PAYLOAD" | yesmem json '.rows | length')
  # Partial-merge upsert: for existing rows, supply only {name, is_default} — session_id and last_used_at are preserved.
  if [ "$EXISTS_COUNT" = "1" ]; then
    UPSERT_PAYLOAD=$(yesmem json -n --arg name "$NAME" '{capability:"rocketchat","action":"upsert","table":"sessions","data":{name:$name,is_default:1}}')
  else
    UPSERT_PAYLOAD=$(yesmem json -n --arg name "$NAME" '{capability:"rocketchat","action":"upsert","table":"sessions","data":{name:$name,session_id:"",is_default:1}}')
  fi
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+sessions?$ ]]; then
  ROWS=$(store '{"capability":"rocketchat","action":"query","table":"sessions","order":"is_default DESC, name ASC"}')
  COUNT=$(echo "$ROWS" | yesmem json '.rows | length')
  if [ -z "$COUNT" ] || [ "$COUNT" = "0" ] || [ "$COUNT" = "null" ]; then
    MSG="Keine Sessions registriert. /yesmem use <name> legt eine an."
  else
    MSG=$(echo "$ROWS" | yesmem json -r '"Sessions (" + (.rows | length | tostring) + "):\n" + ([.rows[] | "  " + .name + (if .is_default == 1 then " (*)" else "" end) + " [" + (.session_id // "fresh") + "]"] | join("\n"))')
  fi
  rc_send "$MSG"
  printf '[%s] reply: /yesmem sessions -> %s\n' "$(date -Is)" "${COUNT:-0}" >> /tmp/rcreply.log
  exit 0
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+status$ ]]; then
  STATUS_ROW=$(store '{"capability":"rocketchat","action":"query","table":"sessions","where":"is_default=1","limit":1}')
  STATUS_NAME=$(echo "$STATUS_ROW" | yesmem json -r '.rows[0].name // empty')
  STATUS_SID=$(echo "$STATUS_ROW" | yesmem json -r '.rows[0].session_id // empty')
  STATUS_USED=$(echo "$STATUS_ROW" | yesmem json -r '.rows[0].last_used_at // empty')
  if [ -z "$STATUS_NAME" ]; then
    MSG="Keine aktive Session. /yesmem use <name> schaltet oder legt an."
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
  rc_send "$MSG"
  printf '[%s] reply: /yesmem status -> %s\n' "$(date -Is)" "${STATUS_NAME:-none}" >> /tmp/rcreply.log
  exit 0
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+use([[:space:]]|$) ]]; then
  MSG=$'Usage: /yesmem use <name> | /yesmem use <name> <session_id>\nName: A-Z a-z 0-9 _ - (max 32).'
  rc_send "$MSG"
  printf '[%s] reply: /yesmem use invalid syntax\n' "$(date -Is)" >> /tmp/rcreply.log
  exit 0
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+model$ ]]; then
  ACTIVE_ROW=$(store '{"capability":"rocketchat","action":"query","table":"sessions","where":"is_default=1","limit":1}')
  ACTIVE_NAME=$(echo "$ACTIVE_ROW" | yesmem json -r '.rows[0].name // empty')
  ACTIVE_MODEL=$(echo "$ACTIVE_ROW" | yesmem json -r '.rows[0].model // empty')
  GLOBAL_MODEL=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["reply_model"],"limit":1}' | yesmem json -r '.rows[0].value // "zai-coding-plan/glm-5.3"')
  if [ -z "$ACTIVE_NAME" ]; then
    MSG="Keine aktive Session. /yesmem use <name> zuerst, dann /yesmem model."
  elif [ -n "$ACTIVE_MODEL" ]; then
    MSG="Aktive Session: ${ACTIVE_NAME}"$'\n'"Model: ${ACTIVE_MODEL} (session-spezifisch)"$'\n'"Globaler Default: ${GLOBAL_MODEL}"
  else
    MSG="Aktive Session: ${ACTIVE_NAME}"$'\n'"Model: ${GLOBAL_MODEL} (globaler Default)"$'\n'"Session-spezifisch setzen via: /yesmem model <name>"
  fi
  rc_send "$MSG"
  printf '[%s] reply: /yesmem model -> %s\n' "$(date -Is)" "${ACTIVE_NAME:-none}" >> /tmp/rcreply.log
  exit 0
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+model[[:space:]]+([A-Za-z0-9_./-]{1,64})$ ]]; then
  MODEL_ARG="${BASH_REMATCH[1]}"
  ACTIVE_ROW=$(store '{"capability":"rocketchat","action":"query","table":"sessions","where":"is_default=1","limit":1}')
  ACTIVE_NAME=$(echo "$ACTIVE_ROW" | yesmem json -r '.rows[0].name // empty')
  if [ -z "$ACTIVE_NAME" ]; then
    MSG="Keine aktive Session. /yesmem use <name> zuerst."
    rc_send "$MSG"
    printf '[%s] reply: /yesmem model failed: no active session\n' "$(date -Is)" >> /tmp/rcreply.log
    exit 0
  fi
  if [[ "$MODEL_ARG" =~ ^(default|clear|reset)$ ]]; then
    CLEAR_PAYLOAD=$(yesmem json -n --arg name "$ACTIVE_NAME" '{capability:"rocketchat","action":"upsert","table":"sessions","data":{name:$name,model:null}}')
    echo "$CLEAR_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
    GLOBAL_MODEL=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["reply_model"],"limit":1}' | yesmem json -r '.rows[0].value // "zai-coding-plan/glm-5.3"')
    MSG="Session ${ACTIVE_NAME}: model cleared → global default (${GLOBAL_MODEL})"
  else
    SET_PAYLOAD=$(yesmem json -n --arg name "$ACTIVE_NAME" --arg model "$MODEL_ARG" '{capability:"rocketchat","action":"upsert","table":"sessions","data":{name:$name,model:$model}}')
    echo "$SET_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
    MSG="Session ${ACTIVE_NAME}: model gesetzt auf ${MODEL_ARG}"
  fi
  rc_send "$MSG"
  printf '[%s] reply: /yesmem model -> %s=%s\n' "$(date -Is)" "$ACTIVE_NAME" "$MODEL_ARG" >> /tmp/rcreply.log
  exit 0
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+model([[:space:]]|$) ]]; then
  MSG=$'Usage: /yesmem model | /yesmem model <name> | /yesmem model default\nName: A-Z a-z 0-9 _ . / - (max 64).'
  rc_send "$MSG"
  printf '[%s] reply: /yesmem model invalid syntax\n' "$(date -Is)" >> /tmp/rcreply.log
  exit 0
elif [[ "$TEXT" =~ ^/yesmem[[:space:]]+models$ ]]; then
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
  rc_send "$MSG"
  printf '[%s] reply: /yesmem models\n' "$(date -Is)" >> /tmp/rcreply.log
  exit 0
elif [[ "$TEXT" =~ ^/yesmem([[:space:]]|$) ]]; then
  MSG=$'Commands: /yesmem use <name> [<session_id>] | /yesmem sessions | /yesmem status | /yesmem model [<name>|default] | /yesmem models'
  rc_send "$MSG"
  printf '[%s] reply: /yesmem help\n' "$(date -Is)" >> /tmp/rcreply.log
  exit 0
fi
shopt -u nocasematch

# --- /yesmem use switch execution (shared by both use branches) ---
if [ -n "$NAME" ] && [ -n "$UPSERT_PAYLOAD" ]; then
  # Set new default FIRST, then reset others — avoids window with no is_default=1 row.
  echo "$UPSERT_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null; done
  OTHERS=$(store '{"capability":"rocketchat","action":"query","table":"sessions","where":"is_default=1","order":"id ASC"}')
  OTHERS_TO_RESET=$(echo "$OTHERS" | yesmem json -r '.rows[] | select(.name != $NAME) | {capability:"rocketchat","action":"upsert","table":"sessions","data":{name:.name,is_default:0}}' --arg NAME "$NAME")
  echo "$OTHERS_TO_RESET" | while IFS= read -r p; do store "$p" > /dev/null; done
  MSG="Aktive Session: ${NAME}"
  if [ -n "$EXPLICIT_SESSION" ]; then MSG="${MSG}"$'\n'"Session: ${EXPLICIT_SESSION}"; fi
  rc_send "$MSG"
  printf '[%s] reply: /yesmem use -> %s\n' "$(date -Is)" "$NAME" >> /tmp/rcreply.log
  exit 0
fi

# --- Normal reply path ---

# Ensure sessions table exists (auto-create if missing — robustness against pre-setup installs).
# Never use list_tables as existence check (Learning #80259: cap_store_meta orphans break it) — query-probe instead.
SESS_PROBE=$(store '{"capability":"rocketchat","action":"query","table":"sessions","limit":1}' 2>&1)
if echo "$SESS_PROBE" | grep -q "does not exist"; then
  store '{"capability":"rocketchat","action":"create_table","table":"sessions","columns":[{"name":"name","type":"TEXT","unique":true},{"name":"session_id","type":"TEXT"},{"name":"is_default","type":"INTEGER"},{"name":"model","type":"TEXT"},{"name":"last_used_at","type":"TEXT"}]}' > /dev/null 2>&1
  printf '[%s] reply: auto-created sessions table\n' "$(date -Is)" >> /tmp/rcreply.log
fi

# reply_errors: persist send/llm failures in the DB (survives /tmp cleanup,
# quota, reboot — the /tmp logs are diagnostics, this is the record of record).
# Auto-create once, mirroring the sessions probe pattern (Learning #80259).
ERR_PROBE=$(store '{"capability":"rocketchat","action":"query","table":"reply_errors","limit":1}' 2>&1)
if echo "$ERR_PROBE" | grep -q "does not exist"; then
  store '{"capability":"rocketchat","action":"create_table","table":"reply_errors","columns":[{"name":"row_id","type":"INTEGER"},{"name":"sender","type":"TEXT"},{"name":"stage","type":"TEXT"},{"name":"message_text","type":"TEXT"},{"name":"http_code","type":"INTEGER"},{"name":"error_body","type":"TEXT"},{"name":"model","type":"TEXT"}]}' > /dev/null 2>&1
  # create_table auto-injects id/created_at/updated_at — do NOT list them in columns (duplicate column error).
  printf '[%s] reply: auto-created reply_errors table\n' "$(date -Is)" >> /tmp/rcreply.log
fi

SESSION_ID=$(store '{"capability":"rocketchat","action":"query","table":"sessions","where":"is_default=1","limit":1}' | yesmem json -r '.rows[0].session_id // empty')

MODEL=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["reply_model"],"limit":1}' | yesmem json -r '.rows[0].value // "zai-coding-plan/glm-5.3"')
# Per-session model overrides global reply_model when set
SESSION_MODEL=$(store '{"capability":"rocketchat","action":"query","table":"sessions","where":"is_default=1","limit":1}' | yesmem json -r '.rows[0].model // empty')
if [ -n "$SESSION_MODEL" ]; then
  MODEL="$SESSION_MODEL"
fi
SYSPROMPT=$(store '{"capability":"rocketchat","action":"query","table":"config","where":"key=?","args":["system_prompt"],"limit":1}' | yesmem json -r '.rows[0].value // "Du bist ein hilfreicher Assistent."')

RESULT=$(llm "$MODEL" "$SYSPROMPT" "Nachricht von $SENDER: $TEXT" "$SESSION_ID")
LLM_EXIT=$?
if [ "$LLM_EXIT" -ne 0 ]; then
  printf '[%s] reply: llm failed exit=%s\n' "$(date -Is)" "$LLM_EXIT" >> /tmp/rcreply.log
  ERR_PAYLOAD=$(yesmem json -n </dev/null --arg rid "${ROW_ID:-0}" --arg sender "${SENDER:-}" --arg stage "llm" --arg code "$LLM_EXIT" --arg body "$RESULT" --arg model "$MODEL" --arg ts "$(date -Is)" '{capability:"rocketchat","action":"upsert","table":"reply_errors","data":{row_id:$rid,sender:$sender,stage:$stage,http_code:$code,error_body:$body,model:$model,created_at:$ts}}')
  echo "$ERR_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null 2>&1; done
  exit 0
fi

if ! echo "$RESULT" | yesmem json -e '.' > /dev/null 2>&1; then
  printf '[%s] reply: invalid llm JSON\n' "$(date -Is)" >> /tmp/rcreply.log
  ERR_PAYLOAD=$(yesmem json -n </dev/null --arg rid "${ROW_ID:-0}" --arg sender "${SENDER:-}" --arg stage "invalid_json" --arg code -1 --arg body "$RESULT" --arg model "$MODEL" --arg ts "$(date -Is)" '{capability:"rocketchat","action":"upsert","table":"reply_errors","data":{row_id:$rid,sender:$sender,stage:$stage,http_code:$code,error_body:$body,model:$model,created_at:$ts}}')
  echo "$ERR_PAYLOAD" | while IFS= read -r p; do store "$p" > /dev/null 2>&1; done
  exit 0
fi

REPLY=$(echo "$RESULT" | yesmem json -r '.result // empty')
REPLY=$(echo "$REPLY" | sed '/^\[[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\} [0-9]\{2\}:[0-9]\{2\}:[0-9]\{2\}\] \[msg:[0-9]\+\]/d')
if [ -z "$REPLY" ]; then
  printf '[%s] reply: empty reply from llm\n' "$(date -Is)" >> /tmp/rcreply.log
  exit 0
fi

NEW_SESSION=$(echo "$RESULT" | yesmem json -r '.session_id // empty')
if [ -n "$NEW_SESSION" ]; then
  ACTIVE_NAME=$(store '{"capability":"rocketchat","action":"query","table":"sessions","where":"is_default=1","limit":1}' | yesmem json -r '.rows[0].name // empty')
  if [ -n "$ACTIVE_NAME" ]; then
    NOW=$(date -Is)
    WB=$(yesmem json -n --arg name "$ACTIVE_NAME" --arg sid "$NEW_SESSION" --arg ts "$NOW" '{capability:"rocketchat","action":"upsert","table":"sessions","data":{name:$name,session_id:$sid,is_default:1,last_used_at:$ts}}')
    echo "$WB" | while IFS= read -r p; do store "$p" > /dev/null; done
  fi
fi

rc_send "$REPLY"
printf '[%s] reply: sent row=%s > %s\n' "$(date -Is)" "$ROW_ID" "${REPLY:0:60}" >> /tmp/rcreply.log
```

## Database

```sql
CREATE TABLE cap_rocketchat__config (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT UNIQUE,
  value TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_rocketchat__updates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  rc_message_id TEXT UNIQUE,
  rid TEXT,
  sender TEXT,
  text TEXT,
  direction TEXT,
  processed INTEGER NOT NULL DEFAULT 0,
  ts TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_rocketchat__sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT UNIQUE,
  session_id TEXT,
  is_default INTEGER,
  last_used_at TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  model TEXT
);
```
