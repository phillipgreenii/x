#!/usr/bin/env bash
# Standalone developer utility — not Nix-wrapped intentionally
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="${SCRIPT_DIR}/.."

case "${1:-}" in
--ci)
  export UL_CI_MODE=true
  shift
  ;;
-h | --help)
  echo "Usage: $0 [--ci]"
  echo "  --ci  Disable laptop-only checks (nix daemon health, time-based cache)"
  exit 0
  ;;
"") ;;
*)
  echo "Unknown argument: $1" >&2
  echo "Usage: $0 [--ci]" >&2
  exit 1
  ;;
esac

# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
# Pin nix-repo-base to the locked rev via the shared resolve-nrb-ref helper
# (pg2-pwfqqn ruling a+c). FAIL CLOSED: there is no unpinned fallback. The helper
# cannot be run without a rev, so the bootstrap reads the rev straight from
# flake.lock and aborts when the node is missing (run `nix flake lock`).
NRB_BOOT_REV=$(jq -er '.nodes."phillipgreenii-nix-base".locked.rev' "${SCRIPT_DIR}/flake.lock") ||
  {
    echo "update-locks.sh: flake.lock has no phillipgreenii-nix-base rev; run 'nix flake lock'" >&2
    exit 1
  }
NRB_REF=$(nix run "github:phillipgreenii/nix-repo-base/${NRB_BOOT_REV}#resolve-nrb-ref" -- --lock "${SCRIPT_DIR}/flake.lock") || exit 1
# Pass WORKSPACE_ROOT so the resolver can prefer the on-disk sibling when present.
export WORKSPACE_ROOT
UL_LIB_DIR="${UL_LIB_DIR:-$(nix run "${NRB_REF}#determine-ul-lib-dir")}"
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-x" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

# Refresh the SHA pins of every `uses: owner/repo@<sha> # <ref>` in .github/workflows
# (ul_refresh_action_pins, shared step from nix-repo-base lib/scripts/update-action-pins-lib.bash;
# beads pg2-ehu9q, pg2-vd804). Mutable refs are reported, not rewritten.
ul_run_step "github-action-pins" \
  "update-locks: refresh GitHub Action SHA pins" \
  ul_refresh_action_pins

ul_finalize
