#!/usr/bin/env bash

# =========================
# Список серверов
# =========================
SERVERS=(
#  "10.8.0.9"
  "10.8.0.10"
#  "10.8.0.11"
#  "10.8.0.16"
#  "10.8.0.17"
)

# =========================
# Настройки
# =========================
SSH_USER="root"

LOCAL_FILE="./inverter-schedule"
LOCAL_MODEL_REGISTRY="./inverter_models.json"
LOCAL_PROFILES_DIR="./device_parameters"
LOCAL_LEGACY_PROFILE="./device_parameters.json"
LOCAL_SERVICE_FILE="./inverter-schedule.service"
REMOTE_DIR="/opt/inverter-schedule"
REMOTE_FILE="${REMOTE_DIR}/inverter-schedule"
SERVICE_NAME="inverter-schedule.service"
REMOTE_SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}"

SSH_PORT="22"

# 0 = бесконечно, пока все не обновятся
MAX_ROUNDS=0

CONNECT_TIMEOUT=20
SSH_ALIVE_INTERVAL=15
SSH_ALIVE_COUNT=4

# HTTP-проверка после запуска сервиса
HEALTHCHECK_URL="http://127.0.0.1:8080/"
HEALTHCHECK_ATTEMPTS=15
HEALTHCHECK_DELAY=1

# Во время долгой передачи scp показываем heartbeat, чтобы было видно,
# что процесс жив. Можно временно переопределить через environment.
TRANSFER_HEARTBEAT_INTERVAL="${TRANSFER_HEARTBEAT_INTERVAL:-5}"
STAGING_DIR="/tmp/inverter-schedule-deploy"

# Для тестов можно указать другой os-release.
# В обычной работе всегда используется /etc/os-release.
OS_RELEASE_FILE="${OS_RELEASE_FILE:-/etc/os-release}"

MODE="deploy"
case "${1:-}" in
  "")
    ;;
  --check)
    MODE="check"
    ;;
  -h|--help)
    cat <<'USAGE'
Использование:
  ./load.sh           Собрать проект и развернуть его на серверах
  ./load.sh --check   Ничего не загружать и не перезапускать; проверить локальные файлы и доступность серверов
USAGE
    exit 0
    ;;
  *)
    echo "Неизвестный аргумент: $1" >&2
    echo "Использование: ./load.sh [--check]" >&2
    exit 2
    ;;
esac

if [ "$#" -gt 1 ]; then
  echo "Слишком много аргументов" >&2
  echo "Использование: ./load.sh [--check]" >&2
  exit 2
fi

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

# =========================
# Проверки локальной системы
# =========================
OS_ID=""
OS_ID_LIKE=""
OS_PRETTY_NAME="Linux"
IS_BAZZITE=0

detect_platform() {
  if [ -r "$OS_RELEASE_FILE" ]; then
    # shellcheck disable=SC1090
    . "$OS_RELEASE_FILE"
    OS_ID="${ID:-}"
    OS_ID_LIKE="${ID_LIKE:-}"
    OS_PRETTY_NAME="${PRETTY_NAME:-${NAME:-Linux}}"
  fi

  case " ${OS_ID} ${OS_ID_LIKE} " in
    *" bazzite "*) IS_BAZZITE=1 ;;
  esac
}

print_dependency_hint() {
  local cmd="$1"

  if [ "$IS_BAZZITE" -eq 1 ]; then
    case "$cmd" in
      sshpass)
        echo "  Bazzite: brew install sshpass"
        ;;
      go)
        echo "  Bazzite: установите/используйте Go в host-окружении; после установки команда 'go' должна быть в PATH"
        ;;
      *)
        echo "  Bazzite: команда '$cmd' должна быть доступна в host-терминале"
        ;;
    esac
    return
  fi

  case " ${OS_ID} ${OS_ID_LIKE} " in
    *" debian "*|*" ubuntu "*)
      case "$cmd" in
        sshpass) echo "  Ubuntu/Debian: apt install -y sshpass" ;;
        *) echo "  Ubuntu/Debian: установите пакет, содержащий '$cmd'" ;;
      esac
      ;;
    *" fedora "*|*" rhel "*|*" centos "*)
      case "$cmd" in
        sshpass) echo "  Fedora/RHEL: dnf install -y sshpass" ;;
        *) echo "  Fedora/RHEL: установите пакет, содержащий '$cmd'" ;;
      esac
      ;;
    *" arch "*)
      case "$cmd" in
        sshpass) echo "  Arch: pacman -S sshpass" ;;
        *) echo "  Arch: установите пакет, содержащий '$cmd'" ;;
      esac
      ;;
    *)
      echo "  Установите '$cmd' средствами вашей системы"
      ;;
  esac
}

check_local_dependencies() {
  local missing=0
  local cmd

  detect_platform
  log "Локальная система: ${OS_PRETTY_NAME}"
  if [ "$IS_BAZZITE" -eq 1 ]; then
    log "Обнаружен Bazzite: используется host-окружение, пакеты автоматически не устанавливаются"
  fi

  for cmd in bash python3 go ssh scp sshpass; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      echo "Не найдена обязательная команда: $cmd" >&2
      print_dependency_hint "$cmd" >&2
      missing=1
    fi
  done

  if [ ! -x ./build.sh ]; then
    echo "Не найден исполняемый ./build.sh" >&2
    missing=1
  fi

  if [ "$missing" -ne 0 ]; then
    return 1
  fi

  return 0
}

run_local_build_or_validation() {
  if [ "$MODE" = "check" ]; then
    log "Режим --check: запускаю проверку build.sh validate без сборки и без деплоя"
    ./build.sh validate
  else
    log "Собираю и проверяю проект перед деплоем: ./build.sh build"
    ./build.sh build
  fi
}

validate_runtime_payload() {
  local required

  for required in "$LOCAL_FILE" "$LOCAL_MODEL_REGISTRY" "$LOCAL_LEGACY_PROFILE" "$LOCAL_SERVICE_FILE"; do
    if [ ! -f "$required" ]; then
      echo "Файл не найден: $required" >&2
      return 1
    fi
  done

  if [ ! -x "$LOCAL_FILE" ]; then
    echo "Бинарный файл не исполняемый: $LOCAL_FILE" >&2
    return 1
  fi

  if [ ! -d "$LOCAL_PROFILES_DIR" ]; then
    echo "Каталог профилей не найден: $LOCAL_PROFILES_DIR" >&2
    return 1
  fi

  python3 - "$LOCAL_MODEL_REGISTRY" "$LOCAL_LEGACY_PROFILE" "$LOCAL_PROFILES_DIR" <<'PY'
import json
import sys
from pathlib import Path

registry_path = Path(sys.argv[1]).resolve()
legacy_path = Path(sys.argv[2]).resolve()
profiles_dir = Path(sys.argv[3]).resolve()
root = registry_path.parent


def load_json(path: Path):
    try:
        with path.open('r', encoding='utf-8') as fh:
            return json.load(fh)
    except FileNotFoundError:
        raise SystemExit(f"Файл профиля не найден: {path}")
    except json.JSONDecodeError as exc:
        raise SystemExit(f"Некорректный JSON {path}: {exc}")

registry = load_json(registry_path)
load_json(legacy_path)

models = registry.get('models')
if not isinstance(models, list) or not models:
    raise SystemExit(f"{registry_path}: отсутствует непустой массив models")

referenced = []
for model in models:
    if not isinstance(model, dict):
        raise SystemExit(f"{registry_path}: элемент models должен быть объектом")
    key = str(model.get('key', '')).strip() or '<без key>'
    rel = str(model.get('parameters_file', '')).strip()
    if not rel:
        raise SystemExit(f"{registry_path}: модель {key}: не указан parameters_file")

    candidate = (root / rel).resolve()
    try:
        candidate.relative_to(root)
    except ValueError:
        raise SystemExit(f"{registry_path}: модель {key}: parameters_file выходит за каталог проекта: {rel}")

    try:
        candidate.relative_to(profiles_dir)
    except ValueError:
        raise SystemExit(
            f"{registry_path}: модель {key}: parameters_file должен находиться внутри "
            f"device_parameters/, иначе load.sh не сможет доставить его: {rel}"
        )

    if not candidate.is_file():
        raise SystemExit(f"Файл профиля не найден для модели {key}: {rel}")

    load_json(candidate)
    referenced.append(candidate)

all_profiles = sorted(profiles_dir.rglob('*.json'))
if not all_profiles:
    raise SystemExit(f"Каталог профилей не содержит JSON: {profiles_dir}")

for profile in all_profiles:
    load_json(profile)

unused = [p for p in all_profiles if p not in referenced]
print(f"Runtime JSON OK: моделей={len(models)}, JSON в device_parameters={len(all_profiles)}")
for model, path in zip(models, referenced):
    print(f"  OK {model.get('key', '<без key>')}: {path.relative_to(root)}")
if unused:
    print("  INFO: в device_parameters есть JSON, не указанные в inverter_models.json; они всё равно будут доставлены:")
    for path in unused:
        print(f"    - {path.relative_to(root)}")
PY
}

if ! check_local_dependencies; then
  exit 1
fi

if ! run_local_build_or_validation; then
  echo "Локальная сборка/проверка завершилась ошибкой" >&2
  exit 1
fi

if ! validate_runtime_payload; then
  echo "Проверка runtime-файлов завершилась ошибкой" >&2
  exit 1
fi

# =========================
# SSH
# =========================
read -rsp "Пароль для SSH: " SSH_PASSWORD
echo

SSH_OPTS=(
#  -p "$SSH_PORT"
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
  -o ConnectTimeout="$CONNECT_TIMEOUT"
  -o ServerAliveInterval="$SSH_ALIVE_INTERVAL"
  -o ServerAliveCountMax="$SSH_ALIVE_COUNT"
  -o NumberOfPasswordPrompts=1
)

run_ssh() {
  local ip="$1"
  local cmd="$2"

  sshpass -p "$SSH_PASSWORD" ssh "${SSH_OPTS[@]}" "${SSH_USER}@${ip}" "$cmd"
}


local_file_info() {
  local path="$1"
  local label="$2"
  local bytes human

  bytes="$(stat -c '%s' "$path" 2>/dev/null || wc -c < "$path")"
  human="$(du -h "$path" 2>/dev/null | awk 'NR==1 {print $1}')"
  [ -n "$human" ] || human="${bytes} bytes"
  printf '  %-32s %10s  (%s bytes)\n' "$label" "$human" "$bytes"
}

run_with_heartbeat() {
  local ip="$1"
  local label="$2"
  shift 2

  local start pid heartbeat_pid rc end elapsed
  start="$(date +%s)"

  "$@" &
  pid=$!

  (
    while sleep "$TRANSFER_HEARTBEAT_INTERVAL"; do
      if ! kill -0 "$pid" 2>/dev/null; then
        exit 0
      fi
      local_now="$(date +%s)"
      local_elapsed=$((local_now - start))
      log "${ip}: ${label}: передача ещё выполняется, прошло ${local_elapsed} сек"
    done
  ) &
  heartbeat_pid=$!

  wait "$pid"
  rc=$?

  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true

  end="$(date +%s)"
  elapsed=$((end - start))

  if [ "$rc" -eq 0 ]; then
    log "${ip}: ${label} завершён за ${elapsed} сек"
  else
    log "${ip}: ERROR: ${label} завершился с кодом ${rc} через ${elapsed} сек"
  fi

  return "$rc"
}

show_upload_diagnostics() {
  local ip="$1"

  log "${ip}: диагностика staging после ошибки передачи:"
  if ! run_ssh "$ip" "
    echo '--- staging ---'
    if [ -d '${STAGING_DIR}' ]; then
      ls -lah '${STAGING_DIR}' || true
      echo '--- staging tree (depth 2) ---'
      find '${STAGING_DIR}' -maxdepth 2 -type f -printf '%p  %s bytes\\n' 2>/dev/null | sort || true
      echo '--- staging total ---'
      du -sh '${STAGING_DIR}' 2>/dev/null || true
    else
      echo '${STAGING_DIR}: отсутствует'
    fi

    echo '--- disk ---'
    df -h /tmp '${REMOTE_DIR}' 2>/dev/null || df -h /tmp || true

    echo '--- memory ---'
    if command -v free >/dev/null 2>&1; then
      free -h || true
    elif [ -r /proc/meminfo ]; then
      head -n 20 /proc/meminfo || true
    else
      echo 'Информация о памяти недоступна'
    fi
  "; then
    log "${ip}: WARNING: не удалось получить удалённую диагностику staging (SSH недоступен)"
  fi
}

# =========================
# Проверка удалённого HTTP
# =========================
remote_http_check() {
  local ip="$1"

  run_ssh "$ip" "
    if command -v curl >/dev/null 2>&1; then
      curl -fsS --max-time 10 '${HEALTHCHECK_URL}' >/dev/null
    elif command -v wget >/dev/null 2>&1; then
      wget -q -T 10 -O /dev/null '${HEALTHCHECK_URL}'
    elif command -v bash >/dev/null 2>&1; then
      bash -lc 'exec 3<>/dev/tcp/127.0.0.1/8080; printf \"GET / HTTP/1.0\\r\\nHost: 127.0.0.1\\r\\nConnection: close\\r\\n\\r\\n\" >&3; IFS= read -r status <&3; case \"\$status\" in HTTP/*) exit 0 ;; *) exit 1 ;; esac'
    else
      exit 127
    fi
  "
}

wait_for_remote_health() {
  local ip="$1"
  local attempt=1

  while [ "$attempt" -le "$HEALTHCHECK_ATTEMPTS" ]; do
    if remote_http_check "$ip" >/dev/null 2>&1; then
      log "${ip}: HTTP-проверка ${HEALTHCHECK_URL} успешна (попытка ${attempt}/${HEALTHCHECK_ATTEMPTS})"
      return 0
    fi
    sleep "$HEALTHCHECK_DELAY"
    attempt=$((attempt + 1))
  done

  log "${ip}: HTTP-проверка ${HEALTHCHECK_URL} не прошла за ${HEALTHCHECK_ATTEMPTS} попыток"
  return 1
}

show_remote_diagnostics() {
  local ip="$1"
  log "${ip}: диагностическая информация сервиса:"
  run_ssh "$ip" "
    systemctl --no-pager -l status '${SERVICE_NAME}' || true
    echo '--- journalctl ---'
    journalctl -u '${SERVICE_NAME}' -n 60 --no-pager || true
  " || true
}

# =========================
# --check: только проверки, без изменений на сервере
# =========================
check_one_server() {
  local ip="$1"

  log "=== ${ip}: --check ==="

  if ! run_ssh "$ip" "
    set -e
    command -v bash >/dev/null 2>&1
    command -v systemctl >/dev/null 2>&1
    command -v install >/dev/null 2>&1
    command -v cp >/dev/null 2>&1
    command -v mv >/dev/null 2>&1
    echo 'SSH: OK'
    echo 'bash: OK'
    echo 'systemctl: OK'
    echo 'coreutils install/cp/mv: OK'
    if [ -d '${REMOTE_DIR}' ]; then
      echo 'runtime_dir: exists'
    else
      echo 'runtime_dir: not installed yet'
    fi
    if [ -f '${REMOTE_SERVICE_FILE}' ]; then
      echo 'service_unit: exists'
    else
      echo 'service_unit: not installed yet'
    fi
  "; then
    log "${ip}: --check не прошёл: SSH недоступен либо отсутствуют bash/systemctl"
    return 1
  fi

  if run_ssh "$ip" "systemctl is-active --quiet '${SERVICE_NAME}'" >/dev/null 2>&1; then
    log "${ip}: сервис ${SERVICE_NAME} активен"
    if remote_http_check "$ip" >/dev/null 2>&1; then
      log "${ip}: HTTP ${HEALTHCHECK_URL} отвечает"
    else
      log "${ip}: WARNING: сервис активен, но HTTP ${HEALTHCHECK_URL} сейчас не отвечает"
    fi
  else
    log "${ip}: INFO: сервис ${SERVICE_NAME} сейчас не активен или ещё не установлен; --check ничего не меняет"
  fi

  log "${ip}: --check успешно завершён"
  return 0
}

if [ "$MODE" = "check" ]; then
  CHECK_FAILED=()
  CHECK_OK=()

  for ip in "${SERVERS[@]}"; do
    if check_one_server "$ip"; then
      CHECK_OK+=("$ip")
    else
      CHECK_FAILED+=("$ip")
    fi
    echo
  done

  if [ "${#CHECK_FAILED[@]}" -gt 0 ]; then
    log "--check завершён с ошибками. Недоступные/неподготовленные серверы:"
    printf ' - %s\n' "${CHECK_FAILED[@]}"
    exit 1
  fi

  log "--check успешно завершён для всех серверов:"
  printf ' - %s\n' "${CHECK_OK[@]}"
  exit 0
fi

# =========================
# Загрузка runtime-набора
# =========================
upload_file() {
  local ip="$1"
  local started finished elapsed profile_count profile_size

  log "${ip}: [1/7] создаю staging ${STAGING_DIR}"
  started="$(date +%s)"
  if ! run_ssh "$ip" "rm -rf '${STAGING_DIR}' && mkdir -p '${STAGING_DIR}'"; then
    elapsed=$(( $(date +%s) - started ))
    log "${ip}: ERROR: не удалось создать staging за ${elapsed} сек"
    return 1
  fi
  elapsed=$(( $(date +%s) - started ))
  log "${ip}: staging создан за ${elapsed} сек"

  log "${ip}: [2/7] отправляю основной runtime"
  local_file_info "$LOCAL_FILE" "inverter-schedule"
  local_file_info "$LOCAL_MODEL_REGISTRY" "inverter_models.json"
  local_file_info "$LOCAL_LEGACY_PROFILE" "device_parameters.json"
  local_file_info "$LOCAL_SERVICE_FILE" "inverter-schedule.service"

  if ! run_with_heartbeat "$ip" "scp runtime" \
    sshpass -p "$SSH_PASSWORD" scp "${SSH_OPTS[@]}" \
      "$LOCAL_FILE" "$LOCAL_MODEL_REGISTRY" "$LOCAL_LEGACY_PROFILE" "$LOCAL_SERVICE_FILE" \
      "${SSH_USER}@${ip}:${STAGING_DIR}/"; then
    show_upload_diagnostics "$ip"
    return 1
  fi
  # Отдельная итоговая строка оставлена короткой, чтобы её легко было найти grep'ом.
  log "${ip}: runtime передан за $(( $(date +%s) - started )) сек от начала staging"

  log "${ip}: [3/7] проверяю основной runtime на сервере"
  if ! run_ssh "$ip" "
    set -e
    for f in inverter-schedule inverter_models.json device_parameters.json inverter-schedule.service; do
      test -s '${STAGING_DIR}/'\"\$f\" || { echo \"ERROR: staging file missing/empty: \$f\" >&2; exit 1; }
      if command -v stat >/dev/null 2>&1; then
        stat -c '%n  %s bytes' '${STAGING_DIR}/'\"\$f\"
      else
        ls -lh '${STAGING_DIR}/'\"\$f\"
      fi
    done
  "; then
    log "${ip}: ERROR: проверка основного runtime в staging не прошла"
    show_upload_diagnostics "$ip"
    return 1
  fi
  log "${ip}: основной runtime в staging проверен"

  profile_count="$(find "$LOCAL_PROFILES_DIR" -type f -name '*.json' | wc -l | tr -d ' ')"
  profile_size="$(du -sh "$LOCAL_PROFILES_DIR" 2>/dev/null | awk 'NR==1 {print $1}')"
  log "${ip}: [4/7] отправляю device_parameters/ (${profile_count} JSON, ${profile_size:-размер неизвестен})"

  if ! run_with_heartbeat "$ip" "scp профилей" \
    sshpass -p "$SSH_PASSWORD" scp "${SSH_OPTS[@]}" -r \
      "$LOCAL_PROFILES_DIR" \
      "${SSH_USER}@${ip}:${STAGING_DIR}/device_parameters"; then
    show_upload_diagnostics "$ip"
    return 1
  fi
  log "${ip}: профили переданы за $(( $(date +%s) - started )) сек от начала staging"

  if ! run_ssh "$ip" "
    set -e
    test -d '${STAGING_DIR}/device_parameters'
    count=\$(find '${STAGING_DIR}/device_parameters' -type f -name '*.json' | wc -l)
    test \"\$count\" -gt 0
    echo \"device_parameters: \$count JSON\"
    du -sh '${STAGING_DIR}/device_parameters' 2>/dev/null || true
    find '${STAGING_DIR}/device_parameters' -type f -name '*.json' -printf '  %f  %s bytes\\n' 2>/dev/null | sort || true
  "; then
    log "${ip}: ERROR: проверка device_parameters/ в staging не прошла"
    show_upload_diagnostics "$ip"
    return 1
  fi

  finished="$(date +%s)"
  log "${ip}: staging полностью готов, общая длительность загрузки $((finished - started)) сек"
  return 0
}

deploy_one_server() {
  local ip="$1"

  log "=== ${ip}: начинаю обновление ==="

  # Сначала полностью загружаем staging. При проблеме с сетью работающий
  # сервис на сервере не останавливается.
  if ! upload_file "$ip"; then
    log "${ip}: не удалось загрузить runtime-файлы, работающий сервис не останавливался; сервер уйдёт в конец очереди"
    return 1
  fi

  log "${ip}: [5/7] останавливаю ${SERVICE_NAME}"
  if ! run_ssh "$ip" "systemctl stop ${SERVICE_NAME}"; then
    log "${ip}: не удалось остановить сервис"
    return 1
  fi

  log "${ip}: [6/7] устанавливаю runtime и перезапускаю systemd service"
  if ! run_ssh "$ip" "
    set -e

    STAGING='/tmp/inverter-schedule-deploy'
    TARGET_DIR='${REMOTE_DIR}'
    TARGET_FILE='${REMOTE_FILE}'
    TARGET_SERVICE='${REMOTE_SERVICE_FILE}'
    STAMP=\$(date +%Y%m%d_%H%M%S)

    mkdir -p \"\$TARGET_DIR\"

    if [ -f \"\$TARGET_FILE\" ]; then
      cp -a \"\$TARGET_FILE\" \"\$TARGET_FILE.bak.\$STAMP\"
    fi
    if [ -f \"\$TARGET_DIR/inverter_models.json\" ]; then
      cp -a \"\$TARGET_DIR/inverter_models.json\" \"\$TARGET_DIR/inverter_models.json.bak.\$STAMP\"
    fi
    if [ -f \"\$TARGET_DIR/device_parameters.json\" ]; then
      cp -a \"\$TARGET_DIR/device_parameters.json\" \"\$TARGET_DIR/device_parameters.json.bak.\$STAMP\"
    fi
    if [ -f \"\$TARGET_SERVICE\" ]; then
      cp -a \"\$TARGET_SERVICE\" \"\$TARGET_SERVICE.bak.\$STAMP\"
    fi

    install -m 0755 \"\$STAGING/inverter-schedule\" \"\$TARGET_FILE\"
    install -m 0644 \"\$STAGING/inverter_models.json\" \"\$TARGET_DIR/inverter_models.json\"
    install -m 0644 \"\$STAGING/device_parameters.json\" \"\$TARGET_DIR/device_parameters.json\"
    install -m 0644 \"\$STAGING/inverter-schedule.service\" \"\$TARGET_SERVICE\"

    rm -rf \"\$TARGET_DIR/device_parameters.new\"
    mkdir -p \"\$TARGET_DIR/device_parameters.new\"
    cp -a \"\$STAGING/device_parameters/.\" \"\$TARGET_DIR/device_parameters.new/\"

    if [ -d \"\$TARGET_DIR/device_parameters\" ]; then
      mv \"\$TARGET_DIR/device_parameters\" \"\$TARGET_DIR/device_parameters.bak.\$STAMP\"
    fi
    mv \"\$TARGET_DIR/device_parameters.new\" \"\$TARGET_DIR/device_parameters\"

    rm -rf \"\$STAGING\"

    chmod +x \"\$TARGET_FILE\"
    if command -v restorecon >/dev/null 2>&1; then
      restorecon -F \"\$TARGET_FILE\" || true
    fi
    chcon -t bin_t \"\$TARGET_FILE\" || true

    systemctl daemon-reload
    systemctl enable '${SERVICE_NAME}' >/dev/null
    systemctl restart '${SERVICE_NAME}'
    systemctl is-active --quiet '${SERVICE_NAME}'
  "; then
    log "${ip}: runtime-файлы установлены, но сервис не запустился или проверка systemd не прошла"
    show_remote_diagnostics "$ip"
    return 1
  fi

  log "${ip}: [7/7] проверяю systemd и HTTP ${HEALTHCHECK_URL}"
  if ! wait_for_remote_health "$ip"; then
    show_remote_diagnostics "$ip"
    return 1
  fi

  log "${ip}: успешно обновлено"
  return 0
}

# =========================
# Основная очередь
# =========================
QUEUE=("${SERVERS[@]}")
DONE=()
ROUND=1

while [ "${#QUEUE[@]}" -gt 0 ]; do
  log "Раунд ${ROUND}. В очереди серверов: ${#QUEUE[@]}"

  NEXT_QUEUE=()

  for ip in "${QUEUE[@]}"; do
    if deploy_one_server "$ip"; then
      DONE+=("$ip")
    else
      NEXT_QUEUE+=("$ip")
    fi

    echo
  done

  QUEUE=("${NEXT_QUEUE[@]}")

  if [ "${#QUEUE[@]}" -gt 0 ]; then
    log "Остались необновлённые серверы:"
    printf ' - %s\n' "${QUEUE[@]}"

    if [ "$MAX_ROUNDS" -ne 0 ] && [ "$ROUND" -ge "$MAX_ROUNDS" ]; then
      log "Достигнут MAX_ROUNDS=${MAX_ROUNDS}, завершаю работу"
      exit 1
    fi

    log "Жду 30 секунд перед повтором..."
    sleep 30
  fi

  ROUND=$((ROUND + 1))
done

log "Все серверы успешно обновлены:"
printf ' - %s\n' "${DONE[@]}"
