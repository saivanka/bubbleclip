#!/bin/bash
# BubbleClip macOS agent — two-way clipboard sync with notifications.
#
#   Cmd+C on the Mac   → dialog: "Copy detected — send to network?"  (auto-send if ASK_SEND=0)
#   Copy elsewhere     → notification: "Copy detected on <device> — captured, Cmd+V to paste"
#                        (or a Capture dialog if ASK_CAPTURE=1)
#
# Usage:
#   BUBBLECLIP_SERVER=http://192.168.1.50:5678 BUBBLECLIP_CODE=XXXX-XXXX ./bubbleclip-agent.sh
#
# Env vars:
#   BUBBLECLIP_SERVER       server URL                     (default http://localhost:5678)
#   BUBBLECLIP_CODE         access code from server logs   (required unless auth is disabled)
#   BUBBLECLIP_DEVICE       device name shown to others    (default: hostname)
#   BUBBLECLIP_INTERVAL     poll interval seconds          (default 1)
#   BUBBLECLIP_ASK_SEND     1 = ask before sending         (default 1)
#   BUBBLECLIP_ASK_CAPTURE  1 = ask before capturing       (default 0 = auto-capture + notify)

SERVER="${BUBBLECLIP_SERVER:-http://localhost:5678}"
CODE="${BUBBLECLIP_CODE:-}"
DEVICE="${BUBBLECLIP_DEVICE:-$(hostname -s)}"
INTERVAL="${BUBBLECLIP_INTERVAL:-1}"
ASK_SEND="${BUBBLECLIP_ASK_SEND:-1}"
ASK_CAPTURE="${BUBBLECLIP_ASK_CAPTURE:-0}"

DEVICE_URL="${DEVICE// /%20}"
echo "BubbleClip agent → $SERVER (device: $DEVICE, ask-send: $ASK_SEND)"

# escape + truncate text for AppleScript strings
esc() { printf '%s' "$1" | head -c 120 | tr '\n\r' '  ' | sed 's/\\/\\\\/g; s/"/\\"/g'; }

notify() { # $1 title, $2 body
  osascript -e "display notification \"$(esc "$2")\" with title \"BubbleClip\" subtitle \"$(esc "$1")\"" >/dev/null 2>&1
}

ask() { # $1 message, $2 action button label → returns 0 if clicked
  osascript -e "display dialog \"$(esc "$1")\" with title \"BubbleClip\" buttons {\"Ignore\",\"$2\"} default button \"$2\" giving up after 15" 2>/dev/null \
    | grep -q "button returned:$2"
}

R_TEXT="" R_ID="" R_DEVICE=""
get_remote() {
  local hdr
  hdr=$(mktemp)
  R_TEXT=$(curl -sf -D "$hdr" -H "X-Access-Code: $CODE" "$SERVER/api/clipboard?plain=1") || { rm -f "$hdr"; return 1; }
  R_ID=$(grep -i '^x-id:' "$hdr" | tr -d '\r' | awk '{print $2}')
  R_DEVICE=$(grep -i '^x-device:' "$hdr" | tr -d '\r' | cut -d' ' -f2-)
  rm -f "$hdr"
}

send_clip() { # $1 text → 0 on success
  curl -sf --data-binary "$1" -H "X-Access-Code: $CODE" "$SERVER/api/clipboard?device=$DEVICE_URL" >/dev/null
}

# fail fast on a bad code so the user isn't silently out of sync
status=$(curl -s -o /dev/null -w '%{http_code}' -H "X-Access-Code: $CODE" "$SERVER/api/clipboard")
if [[ "$status" == "401" || "$status" == "429" ]]; then
  echo "ERROR: server rejected the access code (HTTP $status). Set BUBBLECLIP_CODE to the code shown in the server logs."
  exit 1
fi

last="" last_id=""

# start in sync with the server's current clipboard
if get_remote; then
  last="$R_TEXT"; last_id="$R_ID"
  [[ -n "$R_TEXT" ]] && printf '%s' "$R_TEXT" | pbcopy
fi

while true; do
  local_clip=$(pbpaste 2>/dev/null)

  if [[ "$local_clip" != "$last" ]]; then
    # ---- local Cmd+C detected ----
    if [[ "$ASK_SEND" == "1" ]]; then
      if ask "Copy action detected — send to network?

\"$(esc "$local_clip")\"" "Send"; then
        send_clip "$local_clip" && notify "Sent to network" "$local_clip"
      fi
    else
      send_clip "$local_clip" && notify "Sent to network" "$local_clip"
    fi
    last="$local_clip"                       # don't re-ask for the same copy
    get_remote && last_id="$R_ID"            # don't self-notify

  else
    # ---- check for copies from other devices ----
    if get_remote && [[ -n "$R_ID" && "$R_ID" != "$last_id" ]]; then
      last_id="$R_ID"
      if [[ "$R_TEXT" != "$last" && -n "$R_TEXT" ]]; then
        if [[ "$ASK_CAPTURE" == "1" ]]; then
          if ask "Copy action detected on $R_DEVICE — capture to paste?

\"$(esc "$R_TEXT")\"" "Capture"; then
            printf '%s' "$R_TEXT" | pbcopy
            last="$R_TEXT"
            notify "Captured" "Press Cmd+V to paste"
          fi
        else
          printf '%s' "$R_TEXT" | pbcopy
          last="$R_TEXT"
          notify "Copy detected on $R_DEVICE" "Captured — press Cmd+V to paste"
        fi
      fi
    fi
  fi

  sleep "$INTERVAL"
done
