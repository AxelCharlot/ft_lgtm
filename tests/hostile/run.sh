#!/usr/bin/env bash
#
# Sends every hostile program in this directory to the running backend and
# reports what came back.
#
# Each program names the kind the backend must answer. A program that comes back
# with any other kind is a hole in the sandbox, not a broken test, so this script
# exits non-zero and says which one.
#
# The last thing it does is run a program that works, because a sandbox that
# survives an attack by refusing everything afterwards has not survived it.
#
# Usage:
#   ./run.sh                      against http://lgtm.local/api/run
#   ./run.sh http://host/api/run  against somewhere else
#
# It runs from the guest, where /etc/hosts resolves lgtm.local. From the host it
# needs the three lines of k8s/README.md section 3.

set -euo pipefail

readonly DEFAULT_ENDPOINT='http://lgtm.local/api/run'
readonly ENDPOINT="${1:-${LGTM_ENDPOINT:-${DEFAULT_ENDPOINT}}}"

# One run is bounded by 10 s of compile and 5 s of execution, so 60 s is a
# timeout on the request and never on the program.
readonly REQUEST_TIMEOUT_SECONDS=60

# The kind each program must produce. The kinds come from k8s/README.md
# section 4.
readonly TESTS=(
  'endless_loop.rs:timeout'
  'huge_allocation.rs:runtime'
  'long_output.rs:output_limit'
  'file_read.rs:runtime'
  'network_connect.rs:runtime'
  'syntax_error.rs:compile'
)

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly here

failures=0

log() {
  printf '%s\n' "$*"
}

# Sends one file and prints the whole answer as one line of JSON.
send() {
  local source_file="$1"
  jq --raw-input --slurp --compact-output '{code: .}' "${source_file}" \
    | curl --silent --show-error --max-time "${REQUEST_TIMEOUT_SECONDS}" \
        --request POST "${ENDPOINT}" \
        --header 'Content-Type: application/json' \
        --data-binary @-
}

# Cuts a value to one readable line: no newline, no more than 72 characters.
one_line() {
  local text="$1"
  printf '%s' "${text}" | tr '\n' ' ' | cut -c 1-72
}

run_one() {
  local name="$1"
  local expected="$2"
  local answer observed detail

  if ! answer="$(send "${here}/${name}")"; then
    log "  ${name}"
    log "    the request itself failed against ${ENDPOINT}"
    failures=$(( failures + 1 ))
    return 0
  fi

  observed="$(printf '%s' "${answer}" | jq --raw-output '.error.kind // "none"')"

  # The message on a failure, the output on a success: whichever one carries the
  # reason a reader is looking for.
  if [[ "${observed}" == 'none' ]]; then
    detail="$(printf '%s' "${answer}" | jq --raw-output '.output')"
  else
    detail="$(printf '%s' "${answer}" | jq --raw-output '.error.message')"
  fi

  log "  ${name}"
  log "    expected  ${expected}"
  log "    observed  ${observed}"
  log "    says      $(one_line "${detail}")"

  if [[ "${observed}" != "${expected}" ]]; then
    log "    MISMATCH — the sandbox did not stop this one the way it must"
    failures=$(( failures + 1 ))
  fi
}

# A program that works, sent last. It proves the machine is still standing, and
# it is the difference between a sandbox and a broken backend.
check_the_machine_still_works() {
  local source_file answer output

  source_file="$(mktemp)"
  printf 'fn main() {\n    println!("hello");\n}\n' > "${source_file}"

  log '  a program that works, sent after all six'
  answer="$(send "${source_file}")"
  rm -f "${source_file}"

  output="$(printf '%s' "${answer}" | jq --raw-output '.output')"
  if [[ "${output}" == 'hello' ]]; then
    log '    says      hello'
  else
    log "    MISMATCH — the backend answered $(one_line "${answer}")"
    failures=$(( failures + 1 ))
  fi
}

main() {
  log "Sending the hostile programs to ${ENDPOINT}"
  log ''

  local entry name expected
  for entry in "${TESTS[@]}"; do
    name="${entry%%:*}"
    expected="${entry#*:}"
    run_one "${name}" "${expected}"
    log ''
  done

  check_the_machine_still_works
  log ''

  if (( failures > 0 )); then
    log "${failures} test(s) did not answer what they must."
    return 1
  fi

  log 'Every test was stopped the way it must be, and the machine still works.'
}

main "$@"
