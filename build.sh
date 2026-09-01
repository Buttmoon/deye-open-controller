#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

MODE="${1:-build}"

validate_files() {
  python3 - <<'PY'
import json
from pathlib import Path

root = Path('.')
registry_path = root / 'inverter_models.json'
registry = json.loads(registry_path.read_text(encoding='utf-8'))
models = registry.get('models', [])
assert registry.get('schema_version', 0) > 0, 'invalid schema_version'
assert models, 'models list is empty'
assert sum(bool(m.get('default')) for m in models) == 1, 'exactly one model must be default'

required = {'grid_export_limit', 'grid_charge_enable', 'solar_sell', 'inverter_work_mode', 'use_timer', 'priority_load'}
for point in range(1, 7):
    required |= {
        f'sell_time_point_{point}',
        f'sell_mode_kw_point_{point}',
        f'sell_mode_batt_capacity_{point}',
        f'charge_mode_point_{point}',
    }

for model in models:
    profile_path = root / model['parameters_file']
    data = json.loads(profile_path.read_text(encoding='utf-8'))
    assert data, f'{profile_path}: empty profile'
    codes = {}
    for row in data:
        fields = row.get('fields') or {}
        code = str(fields.get('code', '')).strip().lower()
        assert code, f'{profile_path}: empty code'
        assert code not in codes, f'{profile_path}: duplicate code {code}'
        codes[code] = fields
        assert int(fields.get('register_count', 0)) > 0, f'{profile_path}: {code}: invalid register_count'
        assert str(fields.get('register_type', '')).lower() in {'holding', 'input'}, f'{profile_path}: {code}: invalid register_type'
    missing = sorted(required - set(codes))
    assert not missing, f'{profile_path}: missing schedule codes: {missing}'
    for code in required:
        f = codes[code]
        assert f.get('is_writable') is True, f'{profile_path}: {code} must be writable'
        assert str(f.get('register_type', '')).lower() == 'holding', f'{profile_path}: {code} must be holding'
    print(f'OK {model["key"]}: {len(data)} parameters, {profile_path}')
PY
}

validate_files
python3 scripts/audit_register_profiles.py --root . --json-out REGISTER_AUDIT.json --md-out REGISTER_AUDIT.md

case "$MODE" in
  validate)
    ;;
  test)
    go test ./...
    ;;
  build)
    go test ./...
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o inverter-schedule .
    ;;
  *)
    echo "Usage: $0 [validate|test|build]" >&2
    exit 2
    ;;
esac
