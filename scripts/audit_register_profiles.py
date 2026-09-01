#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from collections import defaultdict
from pathlib import Path
from typing import Any

EXPECTED = {
    "priority_load": 141,
    "inverter_work_mode": 142,
    "grid_export_limit": 143,
    "solar_sell": 145,
    "use_timer": 146,
}
for point in range(1, 7):
    EXPECTED[f"sell_time_point_{point}"] = 147 + point
    EXPECTED[f"sell_mode_kw_point_{point}"] = 153 + point
    EXPECTED[f"sell_mode_batt_capacity_{point}"] = 165 + point
    EXPECTED[f"charge_mode_point_{point}"] = 171 + point

ALLOWED_CHARGE_V105 = {0, 1, 2, 3, 32, 33, 34, 35}
ALLOWED_CHARGE_V104 = {0, 1, 2, 3}


def require(condition: bool, message: str, errors: list[str]) -> None:
    if not condition:
        errors.append(message)


def enum_values(field: dict[str, Any]) -> set[int]:
    values = field.get("enum_values") or []
    return {int(item["value"]) for item in values if isinstance(item, dict) and "value" in item}


def audit_model(root: Path, model: dict[str, Any]) -> dict[str, Any]:
    errors: list[str] = []
    warnings: list[str] = []
    profile_path = root / model["parameters_file"]
    try:
        rows = json.loads(profile_path.read_text(encoding="utf-8"))
    except Exception as exc:
        return {"model_key": model.get("key"), "profile": str(profile_path), "errors": [str(exc)], "warnings": []}

    by_code: dict[str, dict[str, Any]] = {}
    active_addresses: dict[int, list[str]] = defaultdict(list)
    writable_addresses: dict[int, list[str]] = defaultdict(list)
    for index, row in enumerate(rows):
        field = row.get("fields") or {}
        code = str(field.get("code") or "").strip().lower()
        require(bool(code), f"row {index}: empty code", errors)
        if not code:
            continue
        require(code not in by_code, f"duplicate code {code}", errors)
        by_code[code] = field
        address = field.get("modbus_address")
        require(isinstance(address, int) and 0 <= address <= 65535, f"{code}: invalid modbus_address={address}", errors)
        require(int(field.get("register_count") or 0) > 0, f"{code}: invalid register_count", errors)
        require(str(field.get("register_type") or "").lower() in {"holding", "input"}, f"{code}: invalid register_type", errors)
        if field.get("is_active") and isinstance(address, int):
            active_addresses[address].append(code)
        if field.get("is_active") and field.get("is_writable") and isinstance(address, int):
            writable_addresses[address].append(code)
            require(field.get("enforce_bounds") is True, f"{code}: every writable field must enforce logical bounds", errors)
            require(field.get("min") is not None and field.get("max") is not None, f"{code}: writable field has incomplete logical bounds", errors)
            require(field.get("raw_min") is not None and field.get("raw_max") is not None, f"{code}: writable field has incomplete raw bounds", errors)
        if field.get("is_active"):
            require(bool(str(field.get("source") or "").strip()), f"{code}: active field has no source", errors)
            require(bool(str(field.get("confidence") or "").strip()), f"{code}: active field has no confidence", errors)

    for code, address in EXPECTED.items():
        field = by_code.get(code)
        require(field is not None, f"missing required schedule code {code}", errors)
        if field is None:
            continue
        require(field.get("modbus_address") == address, f"{code}: address={field.get('modbus_address')} expected={address}", errors)
        require(field.get("register_type") == "holding", f"{code}: must be holding", errors)
        require(field.get("is_writable") is True, f"{code}: must be writable", errors)
        require(field.get("is_active") is True, f"{code}: must be active", errors)

    key = model["key"]
    expected_charge = ALLOWED_CHARGE_V104 if key.endswith("_v104") else ALLOWED_CHARGE_V105

    for point in range(1, 7):
        time_field = by_code.get(f"sell_time_point_{point}", {})
        require(time_field.get("min") == 0 and time_field.get("max") == 2355 and time_field.get("step") == 5, f"point {point}: invalid HHMM bounds", errors)
        require(time_field.get("raw_min") == 0 and time_field.get("raw_max") == 2355 and time_field.get("enforce_bounds") is True, f"point {point}: invalid raw HHMM bounds", errors)

        power = by_code.get(f"sell_mode_kw_point_{point}", {})
        max_w = int(model["schedule_power_max_w"])
        require(power.get("min") == 0 and power.get("max") == max_w and power.get("step") == 10, f"point {point}: power bounds must be 0..{max_w} W step 10", errors)
        require(power.get("write_scale_factor") == 10 and power.get("schedule_input_scale_factor") == 10, f"point {point}: power write/schedule scale must be 10", errors)
        require(power.get("raw_min") == 0 and power.get("raw_max") == max_w // 10, f"point {point}: power raw bounds invalid", errors)

        soc = by_code.get(f"sell_mode_batt_capacity_{point}", {})
        require(soc.get("min") == 0 and soc.get("max") == 100 and soc.get("raw_min") == 0 and soc.get("raw_max") == 100, f"point {point}: SOC bounds invalid", errors)

        charge = by_code.get(f"charge_mode_point_{point}", {})
        configured_allowed = {int(value) for value in (charge.get("allowed_values") or [])}
        require(enum_values(charge) == expected_charge, f"point {point}: charge bitfield enum invalid: {sorted(enum_values(charge))}, expected {sorted(expected_charge)}", errors)
        require(configured_allowed == expected_charge, f"point {point}: allowed_values invalid: {sorted(configured_allowed)}, expected {sorted(expected_charge)}", errors)
        require(charge.get("raw_min") == min(expected_charge) and charge.get("raw_max") == max(expected_charge), f"point {point}: charge raw bounds invalid", errors)

        voltage = by_code.get(f"prog{point}_voltage", {})
        if "hp3" in str(model.get("protocol") or "").lower() or "v1054" in key or key.endswith("_v104"):
            require(voltage.get("modbus_address") == 159 + point, f"point {point}: voltage address invalid", errors)
            require(voltage.get("scale_factor") == 0.1 and voltage.get("max") == 630 and voltage.get("raw_max") == 6300, f"point {point}: HV voltage must be 0.1 V/raw with protocol max 630V/raw6300", errors)

    export = by_code.get("grid_export_limit", {})
    export_max = int(model["grid_export_max_w"])
    require(export.get("max") == export_max and export.get("write_scale_factor") == 10 and export.get("schedule_input_scale_factor") == 10, f"grid_export_limit must use max={export_max} W and scale 10", errors)
    require(export.get("raw_max") == export_max // 10, "grid_export_limit raw_max invalid", errors)

    priority = by_code.get("priority_load", {})
    require(priority.get("write_mode") == "mapped_masked_bits" and priority.get("write_bitmask") == 3, "priority_load must use mapped masked bits 0-1", errors)
    require(priority.get("write_values") == {"0": 3, "1": 2}, "priority_load mapping must be Load First=3, Battery First=2", errors)
    require(priority.get("allowed_values") == [0, 1], "priority_load allowed_values must be [0, 1]", errors)

    work_mode = by_code.get("inverter_work_mode", {})
    require(work_mode.get("allowed_values") == [0, 1, 2], "inverter_work_mode allowed_values must be [0, 1, 2]", errors)

    timer = by_code.get("use_timer", {})
    require(timer.get("write_mode") == "masked_bits" and timer.get("write_bitmask") == 255, "use_timer must preserve bit8 and modify bits0-7 only", errors)
    require(timer.get("max") == 255 and timer.get("raw_max") == 255, "use_timer bounds invalid", errors)

    battery_wakeup = by_code.get("battery_wake_up", {})
    require(battery_wakeup.get("write_mode") == "masked_bits" and battery_wakeup.get("write_bitmask") == 1,
            "battery_wake_up/register 112 must change only bit0", errors)

    forced_offgrid = by_code.get("off_grid_mode", {})
    require(forced_offgrid.get("write_mode") == "mapped_masked_bits" and forced_offgrid.get("write_bitmask") == 12,
            "off_grid_mode/register 179 must use bits2-3 with read-modify-write", errors)
    require(forced_offgrid.get("write_values") == {"0": 8, "1": 12},
            "off_grid_mode mapping must be disabled=0b10/enabled=0b11 in bits2-3", errors)

    for address, codes in sorted(writable_addresses.items()):
        require(len(codes) == 1, f"writable collision address {address}: {codes}", errors)

    mppt_count = int(model.get("mppt_count") or 0)
    for idx in range(1, 5):
        code = f"pv{idx}_power_3p"
        if code in by_code:
            expected_active = idx <= mppt_count
            require(bool(by_code[code].get("is_active")) == expected_active, f"{code}: active={by_code[code].get('is_active')} expected={expected_active}", errors)

    # Safety-critical manual-write fields. These checks intentionally cover more
    # than the TOU schedule, because periodic tasks and the custom Modbus page
    # use the same profile metadata.
    for code in ("battery_equalization_voltage", "battery_absorption_voltage", "battery_float_voltage"):
        field = by_code.get(code, {})
        require(field.get("scale_factor") == 0.01 and field.get("write_scale_factor") == 0.01, f"{code}: must be 0.01 V/raw", errors)
        require(field.get("raw_min") == 3800 and field.get("raw_max") == 6100, f"{code}: raw bounds must be 3800..6100", errors)
        require(field.get("is_writable") is False, f"{code}: must be read-only in HV profile", errors)

    empty_v = by_code.get("battery_low_voltage", {})
    require(empty_v.get("scale_factor") == 0.01 and empty_v.get("is_writable") is False, "register 103 Empty_v must be read-only and 0.01 V/raw", errors)
    zero_export = by_code.get("zero_export_power", {})
    require(zero_export.get("scale_factor") == 10 and zero_export.get("is_writable") is False, "register 104 ZeroExport must be read-only and HV 10 W/raw", errors)

    equalization_days = by_code.get("battery_equalization_days", {})
    require(equalization_days.get("min") == 0 and equalization_days.get("max") == 90 and
            equalization_days.get("raw_min") == 0 and equalization_days.get("raw_max") == 90,
            "register 105 equalization day cycle must be 0..90 days", errors)
    equalization_hours = by_code.get("battery_equalization_hours", {})
    require(equalization_hours.get("scale_factor") == 0.5 and equalization_hours.get("write_scale_factor") == 0.5 and
            equalization_hours.get("min") == 0 and equalization_hours.get("max") == 10 and
            equalization_hours.get("raw_min") == 0 and equalization_hours.get("raw_max") == 20,
            "register 106 equalization duration must be raw 0..20, 0.5 h/raw", errors)

    for code in ("generator_max_operating_time", "generator_cooling_time"):
        field = by_code.get(code, {})
        require(field.get("scale_factor") == 0.1 and field.get("is_writable") is False,
                f"{code}: undocumented write range must remain read-only, 0.1 h/raw", errors)

    ac_couple_frequency = by_code.get("generator_ac_couple_frz_high", {})
    require(ac_couple_frequency.get("scale_factor") == 0.01 and ac_couple_frequency.get("write_scale_factor") == 0.01 and
            ac_couple_frequency.get("min") == 50 and ac_couple_frequency.get("max") == 65 and
            ac_couple_frequency.get("raw_min") == 5000 and ac_couple_frequency.get("raw_max") == 6500,
            "register 131 AC-couple frequency must be raw 5000..6500, 0.01 Hz/raw", errors)

    ups_delay = by_code.get("ups_delay_time", {})
    require(ups_delay.get("allowed_values") == [0, 1] and ups_delay.get("raw_min") == 0 and ups_delay.get("raw_max") == 1,
            "register 209 UPS delay must allow only 0 or 1", errors)

    current_max = int(model.get("battery_current_max_a") or 0)
    require(0 < current_max <= 185, "battery_current_max_a must be 1..185", errors)
    for code in ("battery_max_charge_current", "battery_max_discharge_current", "generator_charge_battery_current", "grid_charge_battery_current"):
        field = by_code.get(code, {})
        require(field.get("min") == 0 and field.get("max") == current_max, f"{code}: logical range must be 0..{current_max} A", errors)
        require(field.get("raw_min") == 0 and field.get("raw_max") == current_max and field.get("enforce_bounds") is True, f"{code}: raw bounds must be 0..{current_max} A", errors)

    for code in ("battery_shutdown_voltage", "battery_restart_voltage", "battery_low_voltage_cap"):
        field = by_code.get(code, {})
        require(field.get("scale_factor") == 0.1, f"{code}: must be HV 0.1 V/raw", errors)
        require(field.get("raw_min") == 3800 and field.get("raw_max") == 6100, f"{code}: raw bounds must be 3800..6100", errors)
        require(field.get("is_writable") is False, f"{code}: low-level battery protection write must be disabled", errors)

    for code in ("generator_charge_start_voltage", "grid_charge_start_voltage"):
        field = by_code.get(code, {})
        require(field.get("scale_factor") == 0.1 and field.get("write_scale_factor") == 0.1, f"{code}: must be 0.1 V/raw", errors)
        require(field.get("min") == 160 and field.get("max") == 630, f"{code}: safe logical range must be 160..630 V", errors)
        require(field.get("raw_min") == 1600 and field.get("raw_max") == 6300 and field.get("enforce_bounds") is True, f"{code}: raw bounds must be 1600..6300", errors)
        if int(model.get("rated_power_w") or 0) == 60000:
            require(field.get("is_writable") is False, f"{code}: 60 kW profile without exact model code must be read-only", errors)

    min_pv = by_code.get("min_pv_power_for_gen_start", {})
    require(min_pv.get("scale_factor") == 1 and min_pv.get("write_scale_factor") == 1, "register 139 must be 1 W/raw", errors)
    require(min_pv.get("min") == 0 and min_pv.get("max") == 8000 and min_pv.get("raw_min") == 0 and min_pv.get("raw_max") == 8000 and min_pv.get("enforce_bounds") is True, "register 139 bounds must be 0..8000 W", errors)

    rated_w = int(model.get("rated_power_w") or 0)
    for code in ("grid_peak_shaving_power", "max_solar_power"):
        field = by_code.get(code, {})
        require(field.get("scale_factor") == 10 and field.get("write_scale_factor") == 10, f"{code}: must be HV 10 W/raw", errors)
        require(field.get("max") == rated_w and field.get("raw_max") == rated_w // 10 and field.get("enforce_bounds") is True, f"{code}: must be capped at rated_power_w={rated_w}", errors)

    for code in ("grid_standard", "configured_grid_frequency", "configured_grid_phases"):
        field = by_code.get(code, {})
        require(field and field.get("is_writable") is False,
                f"{code}/register 182-184 must remain read-only", errors)

    if key.endswith("_v104"):
        reg144 = by_code.get("external_ct_clamp_phase_v104")
        require(reg144 is not None and reg144.get("modbus_address") == 144 and not reg144.get("is_writable"), "V104 must expose register 144 as read-only external CT clamp phase", errors)
    if key.endswith("_v1054") or "v1054" in key:
        reg144 = by_code.get("default_max_sell_to_grid_power_v105")
        require(reg144 is not None and reg144.get("modbus_address") == 144 and not reg144.get("is_writable") and reg144.get("scale_factor") == 10, "V105.4 must expose register 144 as read-only default max sell-to-grid power, 10 W/raw", errors)

    duplicate_active = {str(k): v for k, v in active_addresses.items() if len(v) > 1}
    if duplicate_active:
        warnings.append("active address aliases exist; writable collisions are forbidden and checked separately")
    if (key.endswith("_v1054") or "v1054" in key) and key != "deye_sun_25k_sg01hp3_eu_am2_v104":
        warnings.append("HV TOU voltage 0.1 V/raw is based on the SG01-HP3-AM2 H/L distinction and must be confirmed read-only on this firmware")

    program_power = by_code.get("sell_mode_kw_point_1", {})
    program_voltage = by_code.get("prog1_voltage", {})
    charge_modes = sorted(int(value) for value in (by_code.get("charge_mode_point_1", {}).get("allowed_values") or []))

    return {
        "model_key": key,
        "model_name": model.get("name"),
        "profile": model["parameters_file"],
        "protocol": model.get("protocol"),
        "profile_variant": model.get("profile_variant"),
        "experimental": bool(model.get("experimental")),
        "parameter_count": len(rows),
        "schedule_power_max_w": model.get("schedule_power_max_w"),
        "grid_export_max_w": model.get("grid_export_max_w"),
        "program_power_read_scale_w": program_power.get("scale_factor"),
        "program_power_write_scale_w": program_power.get("write_scale_factor"),
        "program_voltage_scale_v": program_voltage.get("scale_factor"),
        "program_voltage_max_v": program_voltage.get("max"),
        "allowed_charge_modes": charge_modes,
        "required_tou_addresses": EXPECTED,
        "active_aliases": duplicate_active,
        "errors": errors,
        "warnings": warnings,
        "status": "ok" if not errors else "error",
    }


def markdown_report(results: list[dict[str, Any]]) -> str:
    out = [
        "# Аудит профилей регистров Deye",
        "",
        "Проверка выполняется автоматически скриптом `scripts/audit_register_profiles.py`.",
        "Она проверяет JSON, точные адреса шести программ, масштабы, границы мощности, bitfield-запись и конфликты writable-адресов.",
        "",
        "## Общая карта шести программ",
        "",
        "| Назначение | Адреса |",
        "|---|---:|",
        "| Energy management / Load First / Battery First | 141 |",
        "| Work mode | 142 |",
        "| Max sell / export limit | 143 |",
        "| Solar sell | 145 |",
        "| Time Of Use mask | 146 |",
        "| Время программ 1–6 | 148–153 |",
        "| Мощность программ 1–6 | 154–159 |",
        "| Целевое напряжение программ 1–6 | 160–165 |",
        "| SOC программ 1–6 | 166–171 |",
        "| Grid/Gen/Sell bitfield программ 1–6 | 172–177 |",
        "",
        "## Результаты",
        "",
        "| Профиль | Вариант | Параметров | Лимит программы | Лимит экспорта | Статус |",
        "|---|---|---:|---:|---:|---|",
    ]
    for result in results:
        out.append(f"| `{result['model_key']}` | {result.get('profile_variant') or ''} | {result['parameter_count']} | {result['schedule_power_max_w']} W | {result['grid_export_max_w']} W | **{result['status'].upper()}** |")
    out += [
        "",
        "## Матрица кодирования шести программ",
        "",
        "| Профиль | Power read | Power write | Voltage | Charge modes |",
        "|---|---:|---:|---:|---|",
    ]
    for result in results:
        modes = ",".join(str(v) for v in result.get("allowed_charge_modes") or [])
        out.append(
            f"| `{result['model_key']}` | {result.get('program_power_read_scale_w')} W/raw | "
            f"{result.get('program_power_write_scale_w')} W/raw | {result.get('program_voltage_scale_v')} V/raw, "
            f"max {result.get('program_voltage_max_v')} V | `{modes}` |"
        )
    out += ["", "## Ошибки и предупреждения", ""]
    for result in results:
        out.append(f"### `{result['model_key']}`")
        if result["errors"]:
            out.extend(f"- ERROR: {item}" for item in result["errors"])
        if result["warnings"]:
            out.extend(f"- WARNING: {item}" for item in result["warnings"])
        if not result["errors"] and not result["warnings"]:
            out.append("- Нарушений не найдено.")
        out.append("")
    out += [
        "## Ограничение проверки",
        "",
        "Статический аудит подтверждает внутреннюю согласованность профиля и соответствие использованным протоколам, но не заменяет read-only сверку с конкретным инвертором и его прошивкой. Профили, помеченные `experimental`, нельзя переводить в запись до проверки HMI ↔ Modbus на физическом устройстве.",
        "",
    ]
    return "\n".join(out)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=".")
    parser.add_argument("--json-out")
    parser.add_argument("--md-out")
    args = parser.parse_args()
    root = Path(args.root).resolve()
    registry = json.loads((root / "inverter_models.json").read_text(encoding="utf-8"))
    models = registry.get("models") or []
    results = [audit_model(root, model) for model in models]
    report = {"schema_version": 1, "profile_count": len(results), "ok": all(not r["errors"] for r in results), "results": results}
    if args.json_out:
        Path(args.json_out).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if args.md_out:
        Path(args.md_out).write_text(markdown_report(results), encoding="utf-8")
    for result in results:
        print(f"{result['status'].upper()} {result['model_key']}: {result['parameter_count']} parameters, errors={len(result['errors'])}, warnings={len(result['warnings'])}")
        for error in result["errors"]:
            print(f"  ERROR: {error}")
    return 0 if report["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
