#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="bubbleclip"
APP_URL="http://localhost:9600"
LOG_FILE="$HOME/${APP_NAME}-tunnel.log"
PID_FILE="$HOME/${APP_NAME}-tunnel.pid"

echo "Checking BubbleClip on port 9600..."

if ! curl -fsS --max-time 5 "$APP_URL" >/dev/null; then
    echo "Warning: Nothing responded at $APP_URL"
    echo "Check your Docker container with:"
    echo "docker ps"
    echo "curl -I $APP_URL"
fi

sudo apt-get update
sudo apt-get install -y curl

ARCH="$(dpkg --print-architecture)"

case "$ARCH" in
    amd64)
        PACKAGE_URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb"
        ;;
    arm64)
        PACKAGE_URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm64.deb"
        ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

if ! command -v cloudflared >/dev/null 2>&1; then
    curl -fL "$PACKAGE_URL" -o /tmp/cloudflared.deb
    sudo dpkg -i /tmp/cloudflared.deb
    rm -f /tmp/cloudflared.deb
fi

# Quick Tunnels may conflict with an existing cloudflared config.
for CONFIG in \
    "$HOME/.cloudflared/config.yml" \
    "$HOME/.cloudflared/config.yaml"
do
    if [ -f "$CONFIG" ]; then
        mv "$CONFIG" "${CONFIG}.disabled"
        echo "Temporarily disabled existing config: ${CONFIG}.disabled"
    fi
done

if [ -f "$PID_FILE" ]; then
    OLD_PID="$(cat "$PID_FILE" || true)"
    if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
        kill "$OLD_PID"
        sleep 2
    fi
fi

rm -f "$LOG_FILE"

nohup cloudflared tunnel \
    --no-autoupdate \
    --url "$APP_URL" \
    >"$LOG_FILE" 2>&1 &

echo $! > "$PID_FILE"

echo "Starting Cloudflare tunnel..."

for attempt in $(seq 1 30); do
    PUBLIC_URL="$(grep -oE 'https://[-a-zA-Z0-9]+\.trycloudflare\.com' "$LOG_FILE" | head -n1 || true)"

    if [ -n "$PUBLIC_URL" ]; then
        echo
        echo "BubbleClip is available at:"
        echo "$PUBLIC_URL"
        echo
        echo "Tunnel PID: $(cat "$PID_FILE")"
        echo "Logs: $LOG_FILE"
        echo
        echo "Stop tunnel:"
        echo "kill \$(cat $PID_FILE)"
        exit 0
    fi

    sleep 1
done

echo "The tunnel did not return a URL."
echo "Check the logs:"
cat "$LOG_FILE"
exit 1
