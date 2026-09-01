#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOAD_SH="$ROOT_DIR/load.sh"

PASS=0
FAIL=0

pass() { echo "PASS: $*"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $*" >&2; FAIL=$((FAIL + 1)); }

new_fixture() {
  local dir
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/device_parameters"
  cp "$LOAD_SH" "$dir/load.sh"
  chmod +x "$dir/load.sh"

  cat > "$dir/build.sh" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "${TEST_CALLS:?}/build.calls"
exit 0
SH
  chmod +x "$dir/build.sh"

  printf '#!/bin/sh\nexit 0\n' > "$dir/inverter-schedule"
  chmod +x "$dir/inverter-schedule"
  printf '[]\n' > "$dir/device_parameters.json"
  printf '[]\n' > "$dir/device_parameters/device_parameters_test.json"
  cat > "$dir/inverter_models.json" <<'JSON'
{
  "schema_version": 1,
  "models": [
    {
      "key": "test",
      "default": true,
      "parameters_file": "device_parameters/device_parameters_test.json"
    }
  ]
}
JSON
  cat > "$dir/inverter-schedule.service" <<'UNIT'
[Unit]
Description=Test
[Service]
ExecStart=/opt/inverter-schedule/inverter-schedule
UNIT

  cat > "$dir/bin/sshpass" <<'SH'
#!/usr/bin/env bash
set -e
if [[ "${1:-}" == "-p" ]]; then shift 2; fi
exec "$@"
SH
  cat > "$dir/bin/ssh" <<'SH'
#!/usr/bin/env bash
set -e
echo "$*" >> "${TEST_CALLS:?}/ssh.calls"
cmd="${!#}"
bash -n -c "$cmd"
exit 0
SH
  cat > "$dir/bin/scp" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "${TEST_CALLS:?}/scp.calls"
exit 0
SH
  cat > "$dir/bin/python3" <<'SH'
#!/usr/bin/env bash
exec /usr/bin/python3 "$@"
SH
  cat > "$dir/bin/go" <<'SH'
#!/usr/bin/env bash
exit 0
SH
  cat > "$dir/bin/curl" <<'SH'
#!/usr/bin/env bash
exit 0
SH
  chmod +x "$dir/bin/"*

  echo "$dir"
}

run_check_no_upload_test() {
  local dir calls out rc
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"
  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" PATH="$dir/bin:/usr/bin:/bin" ./load.sh --check 2>&1)"
  rc=$?
  set -e
  if [[ $rc -eq 0 && ! -s "$calls/scp.calls" && "$out" == *"--check"* ]] \
     && ! grep -Eq 'systemctl (stop|start|restart|enable)|install -m|^[[:space:]]*mv[[:space:]]' "$calls/ssh.calls"; then
    pass "--check is non-destructive and performs no scp or remote service mutation"
  else
    fail "--check must not upload or mutate services (rc=$rc, scp_calls=$(wc -l < "$calls/scp.calls")); ssh=$(cat "$calls/ssh.calls"); output: $out"
  fi
  rm -rf "$dir"
}

run_missing_profile_test() {
  local dir calls out rc
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"
  python3 - "$dir/inverter_models.json" <<'PY'
import json, sys
p=sys.argv[1]
d=json.load(open(p))
d['models'][0]['parameters_file']='device_parameters/missing_profile.json'
open(p,'w').write(json.dumps(d))
PY
  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" PATH="$dir/bin:/usr/bin:/bin" ./load.sh --check 2>&1)"
  rc=$?
  set -e
  if [[ $rc -ne 0 && ! -s "$calls/ssh.calls" && "$out" == *"missing_profile.json"* ]]; then
    pass "missing registry-referenced profile fails before SSH"
  else
    fail "missing profile must fail before SSH (rc=$rc, ssh_calls=$(wc -l < "$calls/ssh.calls")); output: $out"
  fi
  rm -rf "$dir"
}

run_bazzite_sshpass_hint_test() {
  local dir calls out rc osr
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  rm -f "$dir/bin/sshpass"
  osr="$dir/os-release"
  cat > "$osr" <<'OS'
NAME="Bazzite"
ID=bazzite
ID_LIKE="fedora"
OS
  set +e
  out="$(cd "$dir" && TEST_CALLS="$calls" OS_RELEASE_FILE="$osr" PATH="$dir/bin:/usr/bin:/bin" ./load.sh --check </dev/null 2>&1)"
  rc=$?
  set -e
  if [[ $rc -ne 0 && "$out" == *"brew install sshpass"* ]]; then
    pass "Bazzite prints Homebrew sshpass installation hint"
  else
    fail "Bazzite missing sshpass must suggest brew install sshpass (rc=$rc); output: $out"
  fi
  rm -rf "$dir"
}



run_profile_outside_deploy_dir_test() {
  local dir calls out rc
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"
  mkdir -p "$dir/other_profiles"
  printf '[]\n' > "$dir/other_profiles/test.json"
  python3 - "$dir/inverter_models.json" <<'PY2'
import json, sys
p=sys.argv[1]
d=json.load(open(p))
d['models'][0]['parameters_file']='other_profiles/test.json'
open(p,'w').write(json.dumps(d))
PY2
  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" PATH="$dir/bin:/usr/bin:/bin" ./load.sh --check 2>&1)"
  rc=$?
  set -e
  if [[ $rc -ne 0 && ! -s "$calls/ssh.calls" && "$out" == *"device_parameters"* ]]; then
    pass "registry cannot reference a profile outside deployed device_parameters directory"
  else
    fail "profile outside device_parameters must fail before SSH (rc=$rc, ssh_calls=$(wc -l < "$calls/ssh.calls")); output: $out"
  fi
  rm -rf "$dir"
}


run_bazzite_full_check_test() {
  local dir calls out rc osr
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"
  osr="$dir/os-release"
  cat > "$osr" <<'OS'
PRETTY_NAME="Bazzite Test"
NAME="Bazzite"
ID=bazzite
ID_LIKE="fedora"
OS
  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" OS_RELEASE_FILE="$osr" PATH="$dir/bin:/usr/bin:/bin" ./load.sh --check 2>&1)"
  rc=$?
  set -e
  if [[ $rc -eq 0 && "$out" == *"Обнаружен Bazzite"* && ! -s "$calls/scp.calls" ]]; then
    pass "Bazzite host check path completes without deployment when dependencies exist"
  else
    fail "Bazzite --check path failed (rc=$rc); output: $out"
  fi
  rm -rf "$dir"
}

run_deploy_payload_and_order_test() {
  local dir calls out rc first_ssh
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"
  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" PATH="$dir/bin:/usr/bin:/bin" ./load.sh 2>&1)"
  rc=$?
  set -e
  first_ssh="$(head -n 1 "$calls/ssh.calls" || true)"
  if [[ $rc -eq 0 ]] && grep -Fxq 'build' "$calls/build.calls" && grep -q 'inverter-schedule.service' "$calls/scp.calls" && [[ "$first_ssh" == *"inverter-schedule-deploy"* ]] && [[ "$first_ssh" != *"systemctl stop"* ]]; then
    pass "deploy builds, uploads service unit, and stages before service stop"
  else
    fail "deploy payload/order incorrect (rc=$rc, first_ssh=$first_ssh, build=$(cat "$calls/build.calls"), scp=$(cat "$calls/scp.calls")); output: $out"
  fi
  rm -rf "$dir"
}

run_check_uses_validate_test() {
  local dir calls out rc
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"
  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" PATH="$dir/bin:/usr/bin:/bin" ./load.sh --check 2>&1)"
  rc=$?
  set -e
  if [[ $rc -eq 0 ]] && grep -Fxq 'validate' "$calls/build.calls" && ! grep -Fxq 'build' "$calls/build.calls"; then
    pass "--check validates without rebuilding"
  else
    fail "--check must call build.sh validate only (rc=$rc, calls=$(cat "$calls/build.calls")); output: $out"
  fi
  rm -rf "$dir"
}


run_deploy_progress_logging_test() {
  local dir calls out rc
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"

  cat > "$dir/bin/scp" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "${TEST_CALLS:?}/scp.calls"
sleep 2
exit 0
SH
  chmod +x "$dir/bin/scp"

  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" TRANSFER_HEARTBEAT_INTERVAL=1 PATH="$dir/bin:/usr/bin:/bin" ./load.sh 2>&1)"
  rc=$?
  set -e

  if [[ $rc -eq 0 \
        && "$out" == *"[1/7]"* \
        && "$out" == *"[2/7]"* \
        && "$out" == *"передача ещё выполняется"* \
        && "$out" == *"runtime передан за"* \
        && "$out" == *"[3/7]"* \
        && "$out" == *"[4/7]"* \
        && "$out" == *"профили переданы за"* \
        && "$out" == *"[5/7]"* \
        && "$out" == *"[6/7]"* \
        && "$out" == *"[7/7]"* ]]; then
    pass "deploy prints step-by-step progress and heartbeat around slow scp"
  else
    fail "deploy must expose scp progress/heartbeat (rc=$rc); output: $out"
  fi
  rm -rf "$dir"
}

run_scp_failure_diagnostics_test() {
  local dir calls out rc
  dir="$(new_fixture)"
  calls="$dir/calls"; mkdir -p "$calls"
  : > "$calls/scp.calls"; : > "$calls/ssh.calls"; : > "$calls/build.calls"

  # Один раунд, чтобы ожидаемая ошибка scp не запускала бесконечные повторы.
  sed -i 's/^MAX_ROUNDS=0$/MAX_ROUNDS=1/' "$dir/load.sh"
  cat > "$dir/bin/scp" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "${TEST_CALLS:?}/scp.calls"
exit 7
SH
  chmod +x "$dir/bin/scp"

  set +e
  out="$(cd "$dir" && printf 'pw\n' | TEST_CALLS="$calls" PATH="$dir/bin:/usr/bin:/bin" ./load.sh 2>&1)"
  rc=$?
  set -e

  if [[ $rc -ne 0 \
        && "$out" == *"ERROR"* \
        && "$out" == *"диагностика staging"* ]] \
     && grep -q "df -h" "$calls/ssh.calls" \
     && grep -q "free -h" "$calls/ssh.calls"; then
    pass "failed scp triggers remote staging/disk/memory diagnostics"
  else
    fail "failed scp must print diagnostics (rc=$rc); ssh=$(cat "$calls/ssh.calls"); output: $out"
  fi
  rm -rf "$dir"
}

run_check_no_upload_test
run_missing_profile_test
run_profile_outside_deploy_dir_test
run_bazzite_sshpass_hint_test
run_bazzite_full_check_test
run_deploy_payload_and_order_test
run_check_uses_validate_test
run_deploy_progress_logging_test
run_scp_failure_diagnostics_test

echo "Tests: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
