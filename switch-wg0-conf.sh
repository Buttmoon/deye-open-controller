#!/usr/bin/env bash

set -u

service="wg-quick@wg0"
target_conf="/etc/wireguard/wg0.conf"
backup_dir="/etc/wireguard/backups"

new_conf="${1:-}"

shift || true
check_hosts=("$@")

if [ "${#check_hosts[@]}" -eq 0 ]; then
  check_hosts=("10.8.0.1" "1.1.1.1" "8.8.8.8")
fi

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

fail() {
  log "ошибка: $*"
  exit 1
}

check_network() {
  for host in "${check_hosts[@]}"; do
    log "проверяю сеть: ping $host"
    if ping -c 3 -W 2 "$host" >/dev/null 2>&1; then
      log "сеть доступна через $host"
      return 0
    fi
  done

  return 1
}

stop_wireguard_force() {
  systemctl stop "$service" >/dev/null 2>&1 || true

  if ip link show wg0 >/dev/null 2>&1; then
    ip link delete wg0 >/dev/null 2>&1 || true
  fi

  systemctl reset-failed "$service" >/dev/null 2>&1 || true
}

start_wireguard() {
  systemctl reset-failed "$service" >/dev/null 2>&1 || true

  if ! systemctl start "$service"; then
    log "systemctl start $service завершился с ошибкой"
    journalctl -u "$service" -n 80 -o cat --no-pager
    return 1
  fi

  return 0
}

rollback() {
  log "возвращаю предыдущий конфиг как $target_conf"

  stop_wireguard_force

  install -m 600 "$backup_conf" "$target_conf"

  if start_wireguard; then
    log "старый конфиг успешно восстановлен"
  else
    log "старый конфиг скопирован, но wireguard не поднялся"
  fi
}

if [ "$(id -u)" -ne 0 ]; then
  fail "запусти скрипт от root"
fi

if [ -z "$new_conf" ]; then
  fail "укажи путь к новому конфигу: $0 /root/keb1.conf"
fi

[ -f "$new_conf" ] || fail "новый конфиг не найден: $new_conf"

mkdir -p "$backup_dir"

if [ -f "$target_conf" ]; then
  backup_conf="$backup_dir/wg0.conf.$(date '+%Y%m%d_%H%M%S').bak"
  log "делаю backup текущего конфига: $backup_conf"
  cp -a "$target_conf" "$backup_conf"
else
  fail "текущий $target_conf не найден, rollback будет невозможен"
fi

log "убираю windows-переносы строк из нового конфига"
sed -i 's/\r$//' "$new_conf"

log "проверяю синтаксис нового wireguard-конфига"
wg-quick strip "$new_conf" >/dev/null || fail "новый конфиг некорректный"

log "останавливаю текущий wireguard"
stop_wireguard_force

log "устанавливаю новый конфиг как $target_conf"
install -m 600 "$new_conf" "$target_conf"

log "запускаю новый конфиг через $service"
if ! start_wireguard; then
  log "новый конфиг не поднялся"
  rollback
  exit 1
fi

sleep 5

if check_network; then
  log "новый конфиг рабочий, включаю автозапуск"
  systemctl enable "$service"
  log "готово: новый конфиг оставлен как $target_conf"
  log "новый VPN IP, судя по конфигу: 10.8.0.9"
  exit 0
else
  log "сеть недоступна после применения нового конфига"
  rollback
  exit 1
fi
