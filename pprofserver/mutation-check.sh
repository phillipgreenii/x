#!/usr/bin/env bash
# Recorded mutation check for the pprofserver test suite.
#
# Proves the suite is strict about the two security-relevant checks by
# applying each mutant and requiring at least one test to FAIL:
#   (a) host-check-loosened: the loopback host switch also accepts 0.0.0.0
#   (b) mode-check-loosened: the unix socket mode 0o600 becomes 0o666
#
# The unmutated suite is run first and must pass, so a failing mutant run can
# only mean the mutant was caught. Mutants are applied through `go test
# -overlay` (a JSON file mapping the real source path to a mutated copy kept
# in a temp dir), so the working tree is never written to and
# `git status --porcelain pprofserver/` is identical on every exit path,
# including a failed or interrupted run.
#
# A mutant is CAUGHT only when go test exits nonzero AND its output contains
# a "--- FAIL" line; a build failure ("[build failed]") does not count.
#
# Exit status: 0 when the baseline passes and both mutants are caught;
# nonzero otherwise (2 baseline failed, 3 mutation site not found, 4 a mutant
# survived), with a message naming the baseline, the mutation site
# that was not found, or the surviving mutant.
set -euo pipefail

# Locate the repo from this script's own path (pprofserver/ is one level down).
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

source_file="$repo_root/pprofserver/pprofserver.go"
test_timeout=300

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/pprofserver-mutation.XXXXXX")"
cleanup() {
  rm -rf "$work_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# run_suite OUTPUT_FILE [OVERLAY_JSON]: run the package tests, bounded by the
# timeout; sets suite_status to go test's exit status (124 on timeout).
suite_status=0
run_suite() {
  local output_file="$1"
  local -a overlay_args=()
  if [[ $# -ge 2 ]]; then
    overlay_args=(-overlay "$2")
  fi
  suite_status=0
  timeout "$test_timeout" nice -n 10 go test -p 4 -count=1 \
    "${overlay_args[@]}" ./pprofserver/ >"$output_file" 2>&1 || suite_status=$?
}

# run_mutant NAME SITE REPLACEMENT: mutate the first SITE occurrence in the
# source to REPLACEMENT and return 0 only when the mutant is caught. Exits
# the script when the mutation site is not found.
run_mutant() {
  local name="$1" site="$2" replacement="$3"
  local content mutated_file overlay_file output_file
  content="$(<"$source_file")"
  if [[ "$content" != *"$site"* ]]; then
    echo "ERROR: mutation site not found for mutant '$name': $site" >&2
    exit 3
  fi
  mutated_file="$work_dir/$name.go"
  overlay_file="$work_dir/$name.overlay.json"
  output_file="$work_dir/$name.out"
  printf '%s\n' "${content/"$site"/"$replacement"}" >"$mutated_file"
  printf '{"Replace":{"%s":"%s"}}\n' "$source_file" "$mutated_file" >"$overlay_file"

  run_suite "$output_file" "$overlay_file"
  if [[ "$suite_status" -eq 0 ]]; then
    echo "SURVIVED: mutant '$name' ($site -> $replacement): tests still pass" >&2
    return 1
  fi
  if grep -q '\[build failed\]' "$output_file"; then
    echo "SURVIVED: mutant '$name' did not build, so it is not caught" >&2
    cat "$output_file" >&2
    return 1
  fi
  if ! grep -q -e '--- FAIL' "$output_file"; then
    echo "SURVIVED: mutant '$name' failed (exit $suite_status) without a '--- FAIL' line" >&2
    cat "$output_file" >&2
    return 1
  fi
  echo "CAUGHT: mutant '$name' ($site -> $replacement): $(grep -c -e '--- FAIL' "$output_file") failing test line(s)"
  return 0
}

echo "Baseline: running the unmutated pprofserver suite"
baseline_output="$work_dir/baseline.out"
run_suite "$baseline_output"
if [[ "$suite_status" -ne 0 ]]; then
  echo "ERROR: baseline run failed (exit $suite_status); no mutants were run" >&2
  cat "$baseline_output" >&2
  exit 2
fi
echo "Baseline: PASS"

survivors=()
run_mutant host-check-loosened \
  'case "127.0.0.1", "localhost":' \
  'case "127.0.0.1", "localhost", "0.0.0.0":' || survivors+=(host-check-loosened)
run_mutant mode-check-loosened \
  'os.Chmod(path, 0o600)' \
  'os.Chmod(path, 0o666)' || survivors+=(mode-check-loosened)

if [[ "${#survivors[@]}" -gt 0 ]]; then
  echo "FAIL: surviving mutant(s): ${survivors[*]}" >&2
  exit 4
fi
echo "OK: both mutants caught (host-check-loosened, mode-check-loosened)"
