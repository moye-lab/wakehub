#!/usr/bin/env bash
# WakeHub Agent 数据卷分离迁移脚本（v0.2.9 问题 1）
#
# 背景：v0.2.9 起 Local Agent 改用独立数据卷 wakehub_agent_data，
# 不再与 Server 共用 wakehub_data（Agent 不应触及数据库文件）。
# 本脚本把旧卷里的 Agent Token 安全复制到新卷，保留内容与 0600 权限。
#
# 用法（在工程目录执行，v0.2.9 包解压后、docker compose up 之前）：
#   bash scripts/migrate-agent-volume.sh
#
# 流程：
#   1. 停 Agent（Server 不停，不影响使用）。
#   2. 备份数据库（scripts/backup.sh）。
#   3. 创建新卷 wakehub_agent_data（已存在则跳过），并把卷根属主改成
#      wakehub(10001)、权限 0700（v0.2.11：docker volume create 建出的
#      卷根是 root 属主 0755，Agent 以 10001 身份改不了它）。
#   4. 用当前 Agent 镜像（Alpine，含 sh/cp/chmod）把旧卷的 agent-token
#      复制到新卷，权限 0600、属主 10001（wakehub 用户）；旧卷无 token
#      文件时跳过（全新安装）。已复制过且内容一致时不再重复复制。
#   5. 校验：新卷 token 存在且 0600、内容与旧卷一致；新卷内无数据库文件。
#   6. 提示执行 docker compose up -d --build 完成升级。
#
# 回滚：新卷未删、旧卷未动；若升级后 Agent 无法连接，按
# docs/MIGRATION-v0.2.9.md 的回滚步骤把 Agent 卷改回 wakehub_data 即可。
# 全程禁止 docker compose down -v（会删库删 token）。
set -euo pipefail

PROJECT="${COMPOSE_PROJECT_NAME:-$(basename "$PWD")}"
OLD_VOL="${PROJECT}_wakehub_data"
NEW_VOL="${PROJECT}_wakehub_agent_data"
AGENT_IMAGE=""

die() { echo "[migrate-agent-volume] 错误：$*" >&2; exit 1; }
info() { echo "[migrate-agent-volume] $*"; }

# 新卷根目录改属主 wakehub(10001) + 权限 0700（v0.2.11）。
# docker volume create 建出的卷根是 root 属主 0755，Agent 以 10001 身份
# 既改不了属主也改不了权限，且重启时不重跑入网逻辑，目录会一直留在
# 0755（问题 11 要求 0700；token 文件本身 0600 不受影响，但目录 0755
# 不合要求）。必须用 --user 0 的一次性容器做（宿主机用户改不了卷内属主）。
# 注意：payload 里路径须带尾斜杠（/new/），假 docker 桩按“容器路径/”
# 改写挂载点，不带斜杠的 /new 不会被改写（实测教训）。
fix_new_volume_perms() {
    info "设置新卷根目录属主与权限（wakehub 0700）……"
    docker run --rm --entrypoint sh --user 0 \
        -v "$NEW_VOL:/new" \
        "$AGENT_IMAGE" -c '
            chown 10001:10001 /new/ && chmod 700 /new/ && \
            [ "$(stat -c %a /new/)" = "700" ] && [ "$(stat -c %u /new/)" = "10001" ] && echo PERMS_OK
        ' | grep -qx PERMS_OK || die "设置新卷权限失败"
}

# 失败自恢复：脚本中途退出时，若 Agent 是被本脚本停掉的，把它恢复运行，
# 避免一次失败就让 Agent 长时间离线。成功退出时不恢复——下一步
# docker compose up -d --build 会统一拉起。
AGENT_STOPPED=0
cleanup() {
    local rc=$?
    if [ "$AGENT_STOPPED" = 1 ] && [ "$rc" -ne 0 ]; then
        echo "[migrate-agent-volume] 迁移失败，正在把 Agent 恢复运行……" >&2
        docker compose start wakehub-agent-local >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || die "未找到 docker 命令"
[ -f docker-compose.yml ] || die "当前目录没有 docker-compose.yml，请先 cd 到 WakeHub 工程目录"

# 复制载体：当前 Agent 容器的镜像（Alpine，含 sh/cp/chmod/chown）；
# 无 Agent 容器时（全新安装）回落为旧版镜像名。
if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx 'wakehub-agent-local'; then
    AGENT_IMAGE="$(docker inspect --format='{{.Config.Image}}' wakehub-agent-local 2>/dev/null || true)"
fi
[ -n "$AGENT_IMAGE" ] || AGENT_IMAGE="wakehub:0.2.11"

if ! docker volume inspect "$OLD_VOL" >/dev/null 2>&1; then
    info "未发现旧卷 $OLD_VOL，视为全新安装：只创建新卷。"
    docker volume create "$NEW_VOL" >/dev/null || die "创建新卷失败"
    fix_new_volume_perms
    info "新卷 $NEW_VOL 已就绪，直接 docker compose up -d 即可。"
    exit 0
fi

info "停 Agent（Server 不停）……"
docker compose stop wakehub-agent-local >/dev/null 2>&1 || true
AGENT_STOPPED=1

if [ -f scripts/backup.sh ]; then
    info "备份数据库……"
    bash scripts/backup.sh backup ./backups || die "备份失败，停止迁移"
fi

info "创建新卷 $NEW_VOL（已存在则跳过）……"
docker volume create "$NEW_VOL" >/dev/null || die "创建新卷失败"
fix_new_volume_perms

info "复制 Agent Token 到新卷（保留内容，权限 0600，属主 wakehub）……"
COPY_OUT="$(docker run --rm --entrypoint sh --user 0 \
    -v "$OLD_VOL:/old:ro" \
    -v "$NEW_VOL:/new" \
    "$AGENT_IMAGE" -c '
        if [ ! -f /old/agent-token ]; then echo NO_TOKEN; exit 0; fi
        if [ -f /new/agent-token ] && cmp -s /old/agent-token /new/agent-token; then echo ALREADY; exit 0; fi
        cp /old/agent-token /new/agent-token && chmod 600 /new/agent-token && chown 10001:10001 /new/agent-token && echo COPIED
    ')" || die "复制 Token 失败"
case "$COPY_OUT" in
    NO_TOKEN) info "旧卷无 agent-token 文件（全新安装或尚未入网），跳过复制。" ;;
    ALREADY)  info "新卷已有相同 Token，跳过复制。" ;;
    COPIED)   info "Token 已复制到新卷。" ;;
    *)        die "复制步骤返回未知结果：$COPY_OUT" ;;
esac

if [ "$COPY_OUT" = "COPIED" ] || [ "$COPY_OUT" = "ALREADY" ]; then
    info "校验新卷……"
    docker run --rm --entrypoint sh --user 0 \
        -v "$OLD_VOL:/old:ro" \
        -v "$NEW_VOL:/new" \
        "$AGENT_IMAGE" -c '
            [ -f /new/agent-token ] || { echo "新卷无 token 文件"; exit 1; }
            [ "$(stat -c %a /new/agent-token)" = "600" ] || { echo "token 权限不是 0600"; exit 1; }
            [ "$(stat -c %u /new/agent-token)" = "10001" ] || { echo "token 属主不是 wakehub(10001)"; exit 1; }
            cmp -s /old/agent-token /new/agent-token || { echo "token 内容与旧卷不一致"; exit 1; }
            [ ! -e /new/wakehub.db ] || { echo "新卷内不应有数据库文件"; exit 1; }
            echo VERIFIED
        ' | grep -qx VERIFIED || die "校验失败"
    info "校验通过：新卷 Token 内容与旧卷一致、0600、属主 wakehub，且无数据库文件。"
fi

info "旧卷 $OLD_VOL 原样保留（未删除），可随时回滚。"
info "下一步：docker compose up -d --build"
info "验证：Agent 在界面显示在线；docker compose logs wakehub-agent-local 应有“已成功连接中心端”。"
