#!/usr/bin/env bash
# aethershell installer — builds Go binaries and installs them.
# Usage: sudo ./install.sh   [--uninstall|--connector-only]
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AETHER="/usr/local/bin/aether"
AETHERD="/usr/local/bin/aetherd"
AETHER_CONNECT="/usr/local/bin/aether-connect"
SYSTEMD_USER_DIR="/etc/systemd/user"
SYSTEMD_UNIT="$SYSTEMD_USER_DIR/aetherd.service"
PROFILE_HOOK="/etc/profile.d/aether.sh"

[ "$(id -u)" -eq 0 ] || { echo "Run as root (sudo ./install.sh)." >&2; exit 1; }

if [ "${1:-}" = "--uninstall" ]; then
	# Stop any running daemon
	pkill aetherd 2>/dev/null || true

	if command -v systemctl >/dev/null 2>&1; then
		systemctl --global disable aetherd.service 2>/dev/null || true
	fi
	rm -f "$AETHER" "$AETHERD" "$AETHER_CONNECT"
	rm -f "$SYSTEMD_UNIT"
	rm -f "$PROFILE_HOOK"
	if command -v systemctl >/dev/null 2>&1; then
		systemctl daemon-reload 2>/dev/null || true
	fi

	# Remove from /etc/shells
	if [ -f /etc/shells ]; then
		grep -vxF "$AETHER" /etc/shells > /etc/shells.tmp 2>/dev/null && mv /etc/shells.tmp /etc/shells || true
	fi

	# Clean up old v1 files
	rm -f /usr/local/bin/persistent-shell /usr/local/bin/persistent-shell-test /etc/tmux-persistent-shell.conf /etc/aether-session.bashrc 2>/dev/null || true

	echo "aethershell removed."
	echo "  (Login shells set to aether are NOT reverted — use chsh manually.)"
	exit 0
fi

# Check for Go
command -v go >/dev/null 2>&1 || { echo "Go is required to build aethershell." >&2; exit 1; }

echo "Building aethershell..."
cd "$SRC_DIR"
mkdir -p bin

if [ "${1:-}" = "--connector-only" ]; then
	go build -o bin/aether-connect ./cmd/aether-connect
	install -m 0755 bin/aether-connect "$AETHER_CONNECT"
	cat <<EOF

aethershell connector installed:
  $AETHER_CONNECT — local-only reconnect wrapper

This install does NOT install aetherd, systemd units, profile hooks, or a login
shell. Use this on a laptop/workstation that only connects to remote aether
hosts.

Examples:
  aether-connect my-server       # Tailscale SSH, no remote sshd required
  aether-connect ts my-server    # same, explicit Tailscale mode
  aether-connect ssh my-server   # OpenSSH mode for public users
EOF
	exit 0
fi

# Build
go build -o bin/aetherd ./cmd/aetherd
go build -o bin/aether  ./cmd/aether
go build -o bin/aether-connect ./cmd/aether-connect

echo "Installing..."
install -m 0755 bin/aetherd "$AETHERD"
install -m 0755 bin/aether  "$AETHER"
install -m 0755 bin/aether-connect "$AETHER_CONNECT"
install -D -m 0644 systemd/user/aetherd.service "$SYSTEMD_UNIT"

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload 2>/dev/null || true
	systemctl --global enable aetherd.service 2>/dev/null || true
fi

# Register as valid login shell (for users who prefer `chsh -s` instead of the
# profile.d hook below).
if [ -f /etc/shells ] && ! grep -qxF "$AETHER" /etc/shells; then
	echo "$AETHER" >> /etc/shells
fi

# Remote-only interception hook.
#
# The login shell stays /bin/bash, so LOCAL console and serial logins are never
# touched. This profile.d snippet only hands off to aether for *remote*
# interactive logins (SSH/Tailscale SSH/login -h all export SSH_CONNECTION).
# AETHER_GEOMETRY is forwarded so an onward `ssh` hop to another aether box can
# inherit the terminal size/orientation.
install -m 0644 /dev/stdin "$PROFILE_HOOK" <<'HOOK'
# aethershell: route REMOTE interactive logins through aether. Local console
# logins (no SSH_CONNECTION) fall straight through to a normal shell.
case $- in
  *i*)
    if [ -n "$SSH_CONNECTION" ] && [ -z "$AETHER_SESSION" ] && \
       [ -z "$TMUX" ] && [ -z "$STY" ] && [ -t 0 ] && \
       command -v aether >/dev/null 2>&1; then
      # Let an onward ssh carry the cached terminal geometry.
      export AETHER_GEOMETRY
      exec aether --login
    fi
    ;;
esac
HOOK

cat <<EOF

aethershell v2 installed:
  $AETHER    — client (login shell wrapper)
  $AETHERD   — daemon (session manager)
  $AETHER_CONNECT — local-only reconnect wrapper
  $SYSTEMD_UNIT — systemd user service
  $PROFILE_HOOK — remote-only login hook

Activation is REMOTE-ONLY: the $PROFILE_HOOK snippet hands off SSH logins to
aether, while local console/serial logins fall through to a plain shell
untouched. No chsh required (and chsh would also catch local console).

To carry terminal geometry across an ssh hop to another aether box, allow the
env var through on BOTH ends:
  client ~/.ssh/config :   SendEnv AETHER_GEOMETRY
  server /etc/ssh/sshd_config : AcceptEnv AETHER_GEOMETRY

Daemon management:
  systemctl --user start aetherd.service
  systemctl --user restart aetherd.service
  journalctl --user -u aetherd.service

How it works:
  aether → Unix socket → aetherd → PTY sessions
  Disconnect and your shell + processes survive.
  Reconnect and you're back exactly where you were.

  aether --list   # list your sessions
  aether --kill <name>  # destroy a session
EOF
