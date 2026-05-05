#!/usr/bin/env bash
# scripts/lib/sandbox-traps.sh — QUM-458 layer 1 helper.
#
# sandbox_install_watchdog spawns a setsid'd companion that polls the test
# driver pid and, if the driver dies (including via SIGKILL which bypasses
# bash EXIT traps), reaps the driver's tmux server and SPRAWL_ROOT directory.
#
# Usage:
#   sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"
#
# Args:
#   $1 driver pid (typically $$)
#   $2 tmux socket basename (may be empty)
#   $3 SPRAWL_ROOT directory (only removed if it lives under /tmp/)
sandbox_install_watchdog() {
    local driver=$1
    local socket=$2
    local root=$3
    (
        setsid bash -c '
            driver=$1; socket=$2; root=$3
            while kill -0 "$driver" 2>/dev/null; do
                sleep 2
            done
            if [ -n "$socket" ]; then
                tmux -L "$socket" kill-server 2>/dev/null || true
                rm -f -- "/tmp/tmux-$(id -u)/$socket" 2>/dev/null || true
            fi
            case "$root" in
                /tmp/*) rm -rf -- "$root" ;;
            esac
        ' _ "$driver" "$socket" "$root"
    ) </dev/null >/dev/null 2>&1 &
    disown
}
