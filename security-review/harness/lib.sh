# shellcheck shell=bash
# Shared helpers for the aethershell adversarial harness. Run as root inside the
# disposable container; drops to victim/attacker via runuser.

VICTIM_UID=2000
ATTACKER_UID=2001
VICTIM_XDG=/run/user/2000
VICTIM_SOCK=$VICTIM_XDG/aethershell/sock
DAEMON_LOG=/tmp/victim-daemon.log

as_victim()   { runuser -u victim   -- env XDG_RUNTIME_DIR=$VICTIM_XDG "$@"; }
as_attacker() { runuser -u attacker -- env XDG_RUNTIME_DIR=/run/user/2001 "$@"; }

hr() { printf '\n========== %s ==========\n' "$*"; }

start_daemon() {
    rm -f "$DAEMON_LOG"
    # start detached, owned by victim, with its own runtime dir
    setsid runuser -u victim -- env XDG_RUNTIME_DIR=$VICTIM_XDG \
        aetherd >"$DAEMON_LOG" 2>&1 < /dev/null &
    for _ in $(seq 1 50); do
        [ -S "$VICTIM_SOCK" ] && break
        sleep 0.1
    done
    if [ ! -S "$VICTIM_SOCK" ]; then
        echo "!! daemon failed to create socket; log:"; cat "$DAEMON_LOG"; return 1
    fi
    echo "daemon up, socket: $VICTIM_SOCK (pid $(pgrep -u victim aetherd | tr '\n' ' '))"
}

stop_daemon() {
    pkill -u victim aetherd 2>/dev/null || true
    sleep 0.3
}

# Drive a session over the socket as victim: create a session, send keystrokes,
# keep the connection alive in the background. $1 = keystrokes to inject.
spawn_session_running() {
    local keys="$1"
    as_victim python3 /harness/drive.py "$VICTIM_SOCK" "$keys" &
    sleep 1.0
}
