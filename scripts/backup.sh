#!/usr/bin/env bash
# WakeHub 备份 / 恢复脚本（M5，审计第 17 项）
#
# 用法：
#   scripts/backup.sh backup [目标目录]        # 备份数据库（+ .env）为 tar.gz
#   scripts/backup.sh restore <备份包> [--include-env] [--force]
#   # 备份包也可以是裸数据库文件（如 Server 启动前自动生成的
#   # wakehub.db.migrate-bak-<时间戳>），restore 会直接用它。
#
# 备份内容：data/wakehub.db（在线安全备份，含 WAL 内容）与 .env（若存在）。
# 备份包权限 0600（内含数据库与密钥，请妥善保管）。
# 恢复前请先停止 WakeHub 服务（docker compose down 或 systemctl stop），
# 恢复后重启服务。恢复时先把当前数据库另存为 .bak-<时间戳>，以便回滚。
# .env 默认不恢复（避免覆盖现役密钥），须显式加 --include-env。
#
# 数据库位置（v0.2.9 起支持两种部署形态）：
#   1. 宿主机文件：WAKEHUB_DB_PATH 指向的 ./data/wakehub.db 存在时直接备份；
#   2. Docker 命名卷：宿主机无此文件时，从命名卷备份（须有 docker 命令；
#      卷名默认为 <工程目录名>_wakehub_data，可用 WAKEHUB_VOLUME 覆盖）。
#      卷内数据库是 WAL 模式：先由一次性容器把 db/-wal/-shm 拷出，再在
#      宿主机用 sqlite3 或 python3 做 integrity_check + 一致备份；
#      校验失败则拒绝打包（大声失败，请停服务后重试）。
#   恢复同样分两种形态：宿主机文件直接覆盖；命名卷则要求 Server 已停，
#   由一次性容器把备份写回卷内（写前把卷内当前库另存为
#   <备份包>.pre-restore-<时间戳>.db）。
#
# 环境变量：
#   WAKEHUB_DB_PATH   数据库路径（默认 ./data/wakehub.db）
#   WAKEHUB_ENV_FILE  .env 路径（默认 ./.env）
#   WAKEHUB_VOLUME    数据卷名（默认 <工程目录名>_wakehub_data）
set -euo pipefail

DB_PATH="${WAKEHUB_DB_PATH:-./data/wakehub.db}"
ENV_FILE="${WAKEHUB_ENV_FILE:-./.env}"
# 宿主机运行脚本的用户：一次性容器以 root 拷出的文件须改回该属主，
# 否则宿主机校验读不到（2026-11-06 晚 VM 实测：拷出的库是 root 属主
# 0600，sg 的 python3 报 unable to open database file）。
HOST_UID="$(id -u)"
HOST_GID="$(id -g)"

die() { echo "[backup] 错误：$*" >&2; exit 1; }
info() { echo "[backup] $*"; }

# 在线安全地拷一份数据库到目标文件。优先 sqlite3 CLI 的 .backup
#（联机一致）；其次 python3 的 sqlite3 backup API；最后退回 cp
#（须停服务，否则 WAL 未落盘的内容可能丢失）。
backup_db_file() {
    local src="$1" dest="$2"
    if command -v sqlite3 >/dev/null 2>&1; then
        sqlite3 "$src" ".backup '$dest'" || die "sqlite3 .backup 失败"
    elif command -v python3 >/dev/null 2>&1; then
        python3 - "$src" "$dest" <<'PYEOF' || die "python3 sqlite3 备份失败"
import sqlite3, sys
src, dest = sys.argv[1], sys.argv[2]
s = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
d = sqlite3.connect(dest)
s.backup(d)
d.close(); s.close()
PYEOF
    else
        echo "[backup] 警告：无 sqlite3/python3，改用 cp——请先停止服务再备份" >&2
        cp "$src" "$dest" || die "cp 数据库失败"
        for suffix in -wal -shm; do
            [ -f "$src$suffix" ] && cp "$src$suffix" "$dest$suffix" || true
        done
    fi
}

# 校验数据库副本一致性：PRAGMA integrity_check 须返回 ok。
# 返回 0 通过；1 损坏/不一致；2 宿主机无 sqlite3/python3，无法校验。
verify_db_copy() {
    local db="$1"
    if command -v sqlite3 >/dev/null 2>&1; then
        [ "$(sqlite3 "$db" "PRAGMA integrity_check;" 2>/dev/null)" = "ok" ]
    elif command -v python3 >/dev/null 2>&1; then
        python3 - "$db" <<'PYEOF'
import sqlite3, sys
c = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
row = c.execute("PRAGMA integrity_check").fetchone()
sys.exit(0 if row and row[0] == "ok" else 1)
PYEOF
    else
        return 2
    fi
}

# 解析一次性容器的 helper 镜像：优先 Agent 容器镜像（Alpine，含 sh/cp），
# 其次 Server 容器镜像；都没有则输出空并返回 1（调用方据此 die）。
docker_helper_image() {
    local img=""
    if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx 'wakehub-agent-local'; then
        img="$(docker inspect --format='{{.Config.Image}}' wakehub-agent-local 2>/dev/null || true)"
    fi
    if [ -z "$img" ] && docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx 'wakehub-server'; then
        img="$(docker inspect --format='{{.Config.Image}}' wakehub-server 2>/dev/null || true)"
    fi
    [ -n "$img" ] || return 1
    echo "$img"
}

# 数据卷名：WAKEHUB_VOLUME 优先，其次 COMPOSE_PROJECT_NAME，最后取工程目录名。
docker_volume_name() {
    echo "${WAKEHUB_VOLUME:-${COMPOSE_PROJECT_NAME:-$(basename "$PWD")}_wakehub_data}"
}

# 从命名卷把数据库文件（含 -wal/-shm，如有）拷到宿主机目录。
copy_db_from_volume() {
    local vol="$1" destdir="$2" helper="$3"
    docker run --rm --entrypoint sh --user 0 -e HOST_UID="$HOST_UID" -e HOST_GID="$HOST_GID" -v "$vol:/data:ro" -v "$destdir:/out" "$helper" -c '
        cp /data/wakehub.db /out/wakehub.db
        for s in -wal -shm; do [ -f "/data/wakehub.db$s" ] && cp "/data/wakehub.db$s" "/out/wakehub.db$s" || true; done
        chown "${HOST_UID:-0}:${HOST_GID:-0}" /out/wakehub.db* 2>/dev/null || true
    ' || die "从数据卷复制数据库失败"
    [ -f "$destdir/wakehub.db" ] || die "数据卷内没有 wakehub.db"
}

# 从命名卷备份数据库到目标文件：拷出 → 校验 → 一致备份。
# scratch 为调用方提供的临时目录（由调用方负责清理）。
backup_db_from_volume() {
    local vol="$1" dest="$2" helper="$3" scratch="$4"
    mkdir -p "$scratch"
    # v0.2.9 问题 11：临时拷贝目录 0700（里面是数据库明文拷贝）。
    chmod 700 "$scratch"
    copy_db_from_volume "$vol" "$scratch" "$helper"
    [ -r "$scratch/wakehub.db" ] || die "拷出的数据库宿主机无法读取：$scratch/wakehub.db（属主/权限异常）"
    local rc=0
    verify_db_copy "$scratch/wakehub.db" || rc=$?
    if [ "$rc" -eq 1 ]; then
        die "从数据卷拷出的数据库校验失败（可能正在写入导致复制撕裂）：请先停掉 Server（docker compose stop wakehub-server）再重试备份"
    elif [ "$rc" -eq 2 ]; then
        die "宿主机无 sqlite3/python3，无法校验卷内数据库备份"
    fi
    backup_db_file "$scratch/wakehub.db" "$dest"
}

# 把数据库文件写回命名卷（恢复用）：清掉旧 -wal/-shm，写新库，权限 0600，
# 属主 10001（容器内 wakehub 用户）。调用前须保证 Server 已停。
write_db_to_volume() {
    local vol="$1" srcdb="$2" helper="$3"
    docker run --rm --entrypoint sh --user 0 -v "$vol:/data" -v "$(dirname "$srcdb"):/in" "$helper" -c '
        rm -f /data/wakehub.db-wal /data/wakehub.db-shm
        cp /in/wakehub.db /data/wakehub.db
        chmod 600 /data/wakehub.db
        chown 10001:10001 /data/wakehub.db
    ' || die "写入数据卷失败"
}

cmd_backup() {
    local dest_dir="${1:-./backups}"
    local use_volume=0 vol="" helper=""
    if [ ! -f "$DB_PATH" ]; then
        # 宿主机无数据库文件：Docker 命名卷部署，先校验前置条件。
        use_volume=1
        command -v docker >/dev/null 2>&1 || die "数据库不存在：$DB_PATH，且未找到 docker，无法从命名卷备份"
        vol="$(docker_volume_name)"
        docker volume inspect "$vol" >/dev/null 2>&1 || die "未找到数据卷 $vol（宿主机也无 $DB_PATH）"
        helper="$(docker_helper_image)" || die "找不到可用的 helper 镜像（需要 wakehub-agent-local 或 wakehub-server 容器）"
    fi
    mkdir -p "$dest_dir" || die "无法创建目标目录：$dest_dir"
    # v0.2.9 问题 11：备份目录 0700（备份包内是数据库与 .env）。
    chmod 700 "$dest_dir"
    local stamp
    stamp="$(date +%Y%m%d-%H%M%S)"
    local work
    work="$(mktemp -d)"
    trap "rm -rf $work" EXIT
    if [ "$use_volume" = 1 ]; then
        info "宿主机无数据库文件，从数据卷 $vol 备份……"
        backup_db_from_volume "$vol" "$work/wakehub.db" "$helper" "$work/volcopy"
    else
        backup_db_file "$DB_PATH" "$work/wakehub.db"
    fi
    if [ -f "$ENV_FILE" ]; then
        cp "$ENV_FILE" "$work/.env"
    else
        info "未找到 $ENV_FILE，备份包内不含 .env"
    fi
    local archive="$dest_dir/wakehub-backup-$stamp.tar.gz"
    tar -czf "$archive" -C "$work" wakehub.db $([ -f "$work/.env" ] && echo .env)
    chmod 600 "$archive"
    info "备份完成：$archive"
}

cmd_restore() {
    local archive="${1:-}"
    shift || true
    local include_env=0 force=0
    for arg in "$@"; do
        case "$arg" in
            --include-env) include_env=1 ;;
            --force) force=1 ;;
            *) die "未知参数：$arg" ;;
        esac
    done
    [ -n "$archive" ] || die "用法：$0 restore <备份包> [--include-env] [--force]"
    [ -f "$archive" ] || die "备份包不存在：$archive"
    if [ "$force" -ne 1 ]; then
        printf "恢复前请确认 WakeHub 服务已停止。继续恢复？[y/N] "
        read -r answer || true
        case "$answer" in
            y|Y|yes|YES) ;;
            *) echo "[backup] 已取消"; exit 0 ;;
        esac
    fi
    local work
    work="$(mktemp -d)"
    trap "rm -rf $work" EXIT
    # 备份包可以是 tar.gz，也可以是裸数据库文件（如 .migrate-bak）。
    if tar -tzf "$archive" >/dev/null 2>&1; then
        tar -xzf "$archive" -C "$work" || die "解包失败：$archive"
    elif [ "$(head -c 15 "$archive" 2>/dev/null)" = "SQLite format 3" ]; then
        info "备份包是裸数据库文件，直接使用"
        cp "$archive" "$work/wakehub.db" || die "复制数据库失败"
    else
        die "备份包既不是 tar.gz 也不是 SQLite 数据库：$archive"
    fi
    [ -f "$work/wakehub.db" ] || die "备份包内没有 wakehub.db"
    if [ -f "$DB_PATH" ]; then
        # 宿主机文件形态：原有流程不变。
        local bak="$DB_PATH.bak-$(date +%Y%m%d-%H%M%S)"
        cp "$DB_PATH" "$bak" || die "备份当前数据库失败"
        info "当前数据库已另存为：$bak"
        for suffix in -wal -shm; do
            [ -f "$DB_PATH$suffix" ] && cp "$DB_PATH$suffix" "$bak$suffix" || true
        done
        mkdir -p "$(dirname "$DB_PATH")"
        # v0.2.9 问题 11：数据恢复到的目录 0700（与服务端启动时一致）。
        chmod 700 "$(dirname "$DB_PATH")"
        cp "$work/wakehub.db" "$DB_PATH" || die "恢复数据库失败"
        rm -f "$DB_PATH-wal" "$DB_PATH-shm"
        chmod 600 "$DB_PATH"
        info "恢复完成：$DB_PATH（请重启 WakeHub 服务）"
    else
        # Docker 命名卷形态：Server 必须已停，再把备份写回卷内。
        command -v docker >/dev/null 2>&1 || die "数据库不在宿主机（$DB_PATH 不存在），且未找到 docker，无法恢复到命名卷"
        local vol helper
        vol="$(docker_volume_name)"
        docker volume inspect "$vol" >/dev/null 2>&1 || die "未找到数据卷 $vol（宿主机也无 $DB_PATH）"
        if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'wakehub-server'; then
            die "Server 正在运行：请先执行 docker compose stop wakehub-server wakehub-agent-local 再恢复"
        fi
        helper="$(docker_helper_image)" || die "找不到可用的 helper 镜像（需要 wakehub-agent-local 或 wakehub-server 容器）"
        # 先把卷内当前库另存为 <备份包>.pre-restore-<时间戳>.db（可回滚）。
        local pre="${archive}.pre-restore-$(date +%Y%m%d-%H%M%S).db"
        local scratch
        scratch="$(mktemp -d)"
        docker run --rm --entrypoint sh --user 0 -e HOST_UID="$HOST_UID" -e HOST_GID="$HOST_GID" -v "$vol:/data:ro" -v "$scratch:/out" "$helper" -c '
            [ -f /data/wakehub.db ] || exit 0
            cp /data/wakehub.db /out/wakehub.db
            for s in -wal -shm; do [ -f "/data/wakehub.db$s" ] && cp "/data/wakehub.db$s" "/out/wakehub.db$s" || true; done
            chown "${HOST_UID:-0}:${HOST_GID:-0}" /out/wakehub.db* 2>/dev/null || true
        ' >/dev/null 2>&1 || true
        if [ -f "$scratch/wakehub.db" ]; then
            cp "$scratch/wakehub.db" "$pre" || die "保存当前数据库失败"
            for suffix in -wal -shm; do
                [ -f "$scratch/wakehub.db$suffix" ] && cp "$scratch/wakehub.db$suffix" "$pre$suffix" || true
            done
            info "卷内当前数据库已另存为：$pre"
        fi
        rm -rf "$scratch"
        # write_db_to_volume 从 $work 目录读 wakehub.db（已是该文件名）。
        write_db_to_volume "$vol" "$work/wakehub.db" "$helper"
        info "恢复完成：数据卷 $vol（请重启 WakeHub 服务）"
    fi
    if [ "$include_env" -eq 1 ] && [ -f "$work/.env" ]; then
        cp "$work/.env" "$ENV_FILE" || die "恢复 .env 失败"
        chmod 600 "$ENV_FILE"
        info ".env 已恢复"
    elif [ -f "$work/.env" ]; then
        info "备份包内含 .env，未恢复（加 --include-env 可恢复）"
    fi
}

case "${1:-}" in
    backup) shift; cmd_backup "${1:-}" ;;
    restore) shift; cmd_restore "$@" ;;
    *) echo "用法：$0 backup [目标目录] | restore <备份包> [--include-env] [--force]" >&2; exit 1 ;;
esac
