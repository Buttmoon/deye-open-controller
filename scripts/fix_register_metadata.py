#!/usr/bin/env python3
"""Corrects register metadata in the bundled Deye profiles.

Register 146 (use_timer) is a bitmask. Its layout is documented inside the
same profiles: the description says "bit0 enable, bit1 Mon ... bit7 Sun; bit8
preserved" and the read-only fields prog_<day>_enabled decode bits 1..7 with
bitmask 2..128. The previous `allowed_values: [0, 1]` contradicted that layout
and made the scheduler reject every weekday mask (e.g. 255 or 0b00111111).

The script is idempotent and only edits the fields listed below.
"""
import json
import sys
from pathlib import Path

DAYS = [
    (1, "Понедельник"), (2, "Вторник"), (3, "Среда"), (4, "Четверг"),
    (5, "Пятница"), (6, "Суббота"), (7, "Воскресенье"),
]

USE_TIMER_BITS = [{
    "bit": 0, "width": 1, "name": "Таймер TOU включён",
    "description": "Общий выключатель программ Time of Use (bit0).",
    "values": {"0": "выключен", "1": "включён"}, "writable": True,
}] + [{
    "bit": bit, "width": 1, "name": f"Программы активны: {label}",
    "description": f"Бит {bit}: программы TOU применяются в день «{label}».",
    "values": {"0": "нет", "1": "да"}, "writable": True,
} for bit, label in DAYS] + [{
    "bit": 8, "width": 1, "name": "Бит 8 (не изменяется приложением)",
    "description": "Сохраняется при записи (write_bitmask=0x00FF). Назначение зависит от прошивки и в профиле не подтверждено.",
    "values": {}, "writable": False,
}]

GRID_PEAK_BITS = [{
    "bit": 4, "width": 2, "name": "Grid Peak Shaving",
    "description": "Биты 4–5: 0b01 = выключено, 0b11 = включено. Остальные биты регистра 178 сохраняются (read-modify-write).",
    "values": {"1": "выключено (0b01)", "3": "включено (0b11)"}, "writable": True,
}]


def fix_profile(path: Path) -> list[str]:
    data = json.loads(path.read_text(encoding="utf-8"))
    changes = []
    for row in data:
        f = row.get("fields") or {}
        code = str(f.get("code", "")).lower()
        if code == "use_timer" and f.get("modbus_address") == 146:
            if "allowed_values" in f:
                changes.append(f"use_timer: removed allowed_values={f['allowed_values']}")
                del f["allowed_values"]
            if f.get("enum_values") is not None:
                changes.append("use_timer: enum_values Off/On replaced by bit definitions")
                f["enum_values"] = None
            if f.get("value_kind") != "bitmask":
                f["value_kind"] = "bitmask"
                changes.append("use_timer: value_kind=bitmask")
            if f.get("bits") != USE_TIMER_BITS:
                f["bits"] = USE_TIMER_BITS
                changes.append("use_timer: explicit bits 0..8")
            f["confidence"] = f.get("confidence") or "manufacturer_protocol_requires_physical_validation"
            note = "Битовая маска: допустимо любое сочетание битов 0–7 (raw 0–255). Ограничение allowed_values=[0,1] было ошибочным."
            if note not in str(f.get("notes", "")):
                f["notes"] = (str(f.get("notes", "")).strip() + " " + note).strip()
        elif code.startswith("prog_") and code.endswith("_enabled") and f.get("modbus_address") == 146:
            if f.get("value_kind") != "boolean":
                f["value_kind"] = "boolean"
                changes.append(f"{code}: value_kind=boolean (single bit)")
        elif code == "grid_peak_shaving_enabled" and f.get("modbus_address") == 178:
            if f.get("value_kind") != "enum":
                f["value_kind"] = "enum"
                changes.append("grid_peak_shaving_enabled: value_kind=enum (mapped bits 4-5)")
            if f.get("bits") != GRID_PEAK_BITS:
                f["bits"] = GRID_PEAK_BITS
                changes.append("grid_peak_shaving_enabled: explicit bits 4-5")
        elif code == "grid_peak_shaving_power" and f.get("modbus_address") == 191:
            if f.get("value_kind") != "scaled":
                f["value_kind"] = "scaled"
                changes.append("grid_peak_shaving_power: value_kind=scaled")
    if changes:
        path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return changes


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    files = sorted((root / "device_parameters").glob("*.json")) + [root / "device_parameters.json"]
    for path in files:
        if not path.exists():
            continue
        changes = fix_profile(path)
        print(f"{path}: {'; '.join(changes) if changes else 'no changes'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
