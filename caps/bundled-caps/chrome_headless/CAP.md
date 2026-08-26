---
name: chrome_headless
description: "Render any URL with headless Chrome: screenshot/png, full-page shot, PDF, dumped DOM, plain-text extraction, plus interactive Playwright runs (click, fill, scrape) via a per-use-case JS module."
version: 2
tags: [web, chrome, playwright, screenshot]
scope: user
tested: true
---

## Purpose

Render any URL with headless Chrome: screenshot/png, full-page shot, PDF, dumped DOM, plain-text extraction, plus interactive Playwright runs (click, fill, scrape) via a per-use-case JS module.

## Scripts

### chrome_headless
kind: tool
schema: {"type":"object","properties":{"sub":{"type":"string","enum":["screenshot","shot-full","pdf","dump","text","run"],"description":"operation"},"url":{"type":"string","description":"target URL"},"out":{"type":"string","description":"output dir; default ~/Downloads/screen"},"viewport":{"type":"string","description":"WxH e.g. 1280x800"},"ua":{"type":"string","description":"user agent override"},"script":{"type":"string","description":"path to JS module for run"}},"required":["sub"]}
sandbox: none
timeout: 120

```bash
  ### --- chrome_headless handler ---
  # args arrive JSON-encoded as $key / $KEY / $ARGS_key (see handleExecuteCap);
  # shell strips the outer quotes but leaves \uXXXX escapes (e.g. & -> \u0026),
  # so decode by re-wrapping as a JSON string literal.
  __j() { printf '%s' "${1:-}" | python3 -c 'import sys,json;print(json.loads("\""+sys.stdin.read().strip()+"\""))' 2>/dev/null || printf '%s' "${1:-}"; }

  SUB=$(__j "${sub:-}")
  URL=$(__j "${url:-}")
  OUT=$(__j "${out:-}")
  VIEW=$(__j "${viewport:-}")
  UA=$(__j "${ua:-}")
  SCRIPT=$(__j "${script:-}")

  # binary + default out dir
  BIN="${CHROME_BIN:-$(command -v google-chrome || command -v google-chrome-stable || echo /usr/bin/google-chrome)}"
  [ -n "$OUT" ] || OUT="$HOME/Downloads/screen"
  mkdir -p "$OUT"
  STAMP=$(date +%s)

  case "$SUB" in
    screenshot|shot-full)
      SLUG=$(printf '%s' "$URL" | tr -cd '[:alnum:].-' | head -c 60)
      FILE="$OUT/${SLUG:-page}-${STAMP}.png"
      FLAGS="--headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --virtual-time-budget=15000"
      [ -z "$UA" ] && FLAGS="$FLAGS" || FLAGS="$FLAGS --user-agent=$(printf '%s' "$UA" | sed 's/ /%20/g')"
      if [ "$SUB" = "screenshot" ]; then
        [ -z "$VIEW" ] && VIEW="1280,800"
        "$BIN" $FLAGS --screenshot="$FILE" --window-size="$VIEW" "$URL" >/dev/null 2>&1
      else
        "$BIN" $FLAGS --screenshot="$FILE" --hide-scrollbars "$URL" >/dev/null 2>&1
      fi
      if [ -s "$FILE" ]; then
        printf '{"ok":true,"sub":"%s","file":"%s","bytes":%s}\n' "$SUB" "$FILE" "$(stat -c%s "$FILE")"
      else
        printf '{"ok":false,"error":"chrome screenshot produced no file"}\n'
      fi
      ;;
    pdf)
      SLUG=$(printf '%s' "$URL" | tr -cd '[:alnum:].-' | head -c 60)
      FILE="$OUT/${SLUG:-page}-${STAMP}.pdf"
      "$BIN" --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --no-pdf-header-footer --virtual-time-budget=15000 --print-to-pdf="$FILE" "$URL" >/dev/null 2>&1
      if [ -s "$FILE" ]; then
        printf '{"ok":true,"sub":"pdf","file":"%s","bytes":%s}\n' "$FILE" "$(stat -c%s "$FILE")"
      else
        printf '{"ok":false,"error":"chrome pdf produced no file"}\n'
      fi
      ;;
    dump)
      HTML=$("$BIN" --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --virtual-time-budget=15000 --dump-dom "$URL" 2>/dev/null)
      SLUG=$(printf '%s' "$URL" | tr -cd '[:alnum:].-' | head -c 60)
      FILE="$OUT/${SLUG:-page}-${STAMP}.html"
      printf '%s' "$HTML" > "$FILE"
      printf '{"ok":true,"sub":"dump","file":"%s","bytes":%s}\n' "$FILE" "${#HTML}"
      ;;
    text)
      "$BIN" --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --virtual-time-budget=15000 --dump-dom "$URL" 2>/dev/null | python3 -c 'import sys,re,html;t=sys.stdin.read();t=re.sub("(?is)<(script|style|noscript)[^>]*>.*?</\\1>"," ",t);t=re.sub("(?s)<[^>]+>"," ",t);t=html.unescape(t);t=re.sub("\\s+"," ",t);print(t.strip()[:4000])'
      ;;
    run)
      [ -n "$SCRIPT" ] || { printf '{"ok":false,"error":"run requires script=<path.js>"}\n'; exit 0; }
      RUNJS="$HOME/.claude/caps/chrome_headless/run.js"
        mkdir -p "$(dirname "$RUNJS")"
        echo Ly8gY2hyb21lX2hlYWRsZXNzIHJ1biB3cmFwcGVyOiBsYXVuY2hlcyBzeXN0ZW0gQ2hyb21lIHZpYSBQbGF5d3JpZ2h0LAovLyBoYW5kcyBhIHJlYWR5IHBhZ2UgdG8gdGhlIHVzZXIgc2NyaXB0LCBjYXB0dXJlcyBjb25zb2xlL25ldHdvcmssIHJldHVybnMgSlNPTi4KY29uc3QgeyBjaHJvbWl1bSB9ID0gcmVxdWlyZSgncGxheXdyaWdodCcpOwoKKGFzeW5jICgpID0+IHsKICBjb25zdCBbLCAsIHNjcmlwdFBhdGgsIHZpZXdwb3J0QXJnLCB1YUFyZ10gPSBwcm9jZXNzLmFyZ3Y7CiAgY29uc3QgbW9kID0gcmVxdWlyZShyZXF1aXJlKCdwYXRoJykucmVzb2x2ZShwcm9jZXNzLmN3ZCgpLCBzY3JpcHRQYXRoKSk7CiAgY29uc3QgZm4gPSB0eXBlb2YgbW9kID09PSAnZnVuY3Rpb24nID8gbW9kIDogKG1vZC5kZWZhdWx0IHx8IG1vZC5oYW5kbGVyKTsKICBjb25zdCBbdyA9IDEyODAsIGggPSA4MDBdID0gKHZpZXdwb3J0QXJnIHx8ICcxMjgwLDgwMCcpLnNwbGl0KC9beCxdLykubWFwKE51bWJlcik7CgogIGNvbnN0IGJyb3dzZXIgPSBhd2FpdCBjaHJvbWl1bS5sYXVuY2goewogICAgaGVhZGxlc3M6IHRydWUsCiAgICBleGVjdXRhYmxlUGF0aDogcHJvY2Vzcy5lbnYuQ0hST01FX1BBVEggfHwgJy91c3IvYmluL2dvb2dsZS1jaHJvbWUnLAogICAgYXJnczogWyctLW5vLXNhbmRib3gnLCAnLS1kaXNhYmxlLXNldHVpZC1zYW5kYm94JywgJy0tZGlzYWJsZS1kZXYtc2htLXVzYWdlJywgJy0tZGlzYWJsZS1ncHUnLCAnLS1uby16eWdvdGUnXQogIH0pOwogIHRyeSB7CiAgICBjb25zdCBjdHggPSBhd2FpdCBicm93c2VyLm5ld0NvbnRleHQoewogICAgICB2aWV3cG9ydDogeyB3aWR0aDogdywgaGVpZ2h0OiBoIH0sCiAgICAgIHVzZXJBZ2VudDogdWFBcmcgfHwgdW5kZWZpbmVkCiAgICB9KTsKICAgIGNvbnN0IHBhZ2UgPSBhd2FpdCBjdHgubmV3UGFnZSgpOwogICAgY29uc3QgZXZlbnRzID0geyBjb25zb2xlOiBbXSwgcmVxdWVzdHM6IFtdIH07CiAgICBwYWdlLm9uKCdjb25zb2xlJywgbSA9PiBldmVudHMuY29uc29sZS5wdXNoKHsgdHlwZTogbS50eXBlKCksIHRleHQ6IG0udGV4dCgpIH0pKTsKICAgIHBhZ2Uub24oJ3JlcXVlc3RmYWlsZWQnLCByID0+IGV2ZW50cy5yZXF1ZXN0cy5wdXNoKHsgdXJsOiByLnVybCgpLCBlcnJvcjogci5mYWlsdXJlKCkgJiYgci5mYWlsdXJlKCkuZXJyb3JUZXh0IH0pKTsKICAgIHBhZ2Uub24oJ3Jlc3BvbnNlJywgciA9PiB7IGlmIChyLnN0YXR1cygpID49IDQwMCkgZXZlbnRzLnJlcXVlc3RzLnB1c2goeyB1cmw6IHIudXJsKCksIHN0YXR1czogci5zdGF0dXMoKSB9KTsgfSk7CiAgICBjb25zdCByZXN1bHQgPSBhd2FpdCBmbih7IHBhZ2UsIGJyb3dzZXI6IGN0eCwgdXJsOiBwcm9jZXNzLmVudi5DQVBfVVJMIHx8ICcnLCBvdXREaXI6IHByb2Nlc3MuZW52LkNBUF9PVVQgfHwgJycgfSk7CiAgICBjb25zb2xlLmxvZyhKU09OLnN0cmluZ2lmeSh7IG9rOiB0cnVlLCByZXN1bHQsIGV2ZW50cyB9KSk7CiAgfSBmaW5hbGx5IHsKICAgIGF3YWl0IGJyb3dzZXIuY2xvc2UoKS5jYXRjaCgoKSA9PiB7fSk7CiAgfQogIHByb2Nlc3MuZXhpdCgwKTsKfSkoKS5jYXRjaChlID0+IHsKICBjb25zb2xlLmxvZyhKU09OLnN0cmluZ2lmeSh7IG9rOiBmYWxzZSwgZXJyb3I6IFN0cmluZygoZSAmJiBlLm1lc3NhZ2UpIHx8IGUpIH0pKTsKICBwcm9jZXNzLmV4aXQoMSk7Cn0pOwo= | base64 -d > "$RUNJS"
      RES=$(NODE_PATH="$HOME/node_modules" CAP_URL="$URL" CAP_OUT="$OUT" node "$RUNJS" "$SCRIPT" "$VIEW" "$UA" 2>/dev/null)
      if [ $? -ne 0 ] || [ -z "$RES" ]; then
        printf '{"ok":false,"error":"node/playwright failed for %s"}\n' "$SCRIPT"
        exit 0
      fi
      printf '%s\n' "$RES"
      ;;
    *)
      printf '{"ok":false,"error":"unknown sub %s (screenshot|shot-full|pdf|dump|text|run)"}\n' "$SUB"
      ;;
  esac
```
