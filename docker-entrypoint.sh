#!/bin/sh
# WakeHub 单容器入口（v0.3.2）：同一容器内同时运行 Server 与本地 Agent。
#
# 用法（唯一推荐的单容器部署）：
#   docker run -d --name=wakehub --restart unless-stopped --network host \
#     --cap-add NET_RAW \
#     -e WAKEHUB_ADMIN_PASSWORD=你的密码 \
#     -v ./data:/app/data moyelab/wakehub:0.3.2
#
# 权限模型（v0.3.1 改动，修 v0.3.0 的 bind mount 权限 bug）：
#   入口本身以 root 启动：先把数据目录属主改成 wakehub(10001)、权限
#   0700（已是正确属主时跳过），再解析密钥并写文件（属主 wakehub、
#   0600），最后用 su-exec 把 Server 与 Agent 降权到 wakehub 身份运行。
#   长驻进程仍以非 root 运行，安全姿态与 v0.3.0 一致；只有启动瞬间
#   的准备工作是 root。bind mount 的宿主机目录属主不对时（如 root
#   账号下 ./data 属主是 root），不用再手动 chown，entrypoint 自动修。
#   v0.3.0 的教训：容器以 wakehub(10001) 直接启动时，root 属主的
#   ./data 写不进，密钥写失败、Server 初始化数据库失败，容器无限
#   重启循环（VM 真机 2026-11-07 实测）。
#
# 密钥解析规则（env 优先，文件次之，缺失则生成；解析出的值一律写回
# 文件，保证重启后与首次一致）：
#   WAKEHUB_ADMIN_PASSWORD  管理员密码：env（非占位值）> 文件
#                           .admin_password > 随机生成并打印。
#                           注意：密码只在首次启动建账号时生效；已建
#                           账号后改密码走网页改密或 reset-password。
#   WAKEHUB_SECRET_KEY      会话/SSH 加密密钥：env（非占位值）> 文件
#                           .secret_key > 随机生成。换值会使已存的
#                           SSH 密码无法解密，故一经生成即持久化。
#   WAKEHUB_AGENT_ENROLL_TOKEN
#                           本地 Agent 入网令牌：env（非占位值）> 文件
#                           .enroll_token > 随机生成。Server 与 Agent
#                           共用同一值（回环自动批准的前提）。
#
# 失败策略（v0.3.1）：任何一步写失败立即向 stderr 报错并 exit 1，
# 绝不打印虚假的“已保存”。v0.3.0 的误报教训：写失败时 `set -e` 在
# 命令替换里不一定生效（BusyBox ash 与 dash 行为不一致），“已随机
# 生成并保存到数据卷”照常打印，掩盖了真正的问题。v0.3.1 起
# resolve_secret 不再用命令替换返回值，改走全局变量 + 显式检查。
#
# 进程监督：Server 与 Agent 双双后台启动，任一退出即杀掉另一个、
# 整个容器退出（--restart unless-stopped 会重拉整个容器）。
# SIGTERM/SIGINT 转发给两个子进程。
#
# 注意：本脚本只在“默认入口”（单容器模式）下运行；docker-compose
# 的 server/agent 两个服务会显式覆盖 entrypoint，各自只跑一个进程，
# 不走本脚本（且 compose 服务用 user: "10001:10001" 跑成 wakehub，
# 不经过本脚本的 root 阶段）。

set -eu

DATA_DIR="${WAKEHUB_DATA_DIR:-/app/data}"
# resolve_secret 的返回值走全局变量（不用命令替换，见上文失败策略）。
RESOLVED_VALUE=""

# --- 数据目录：属主 wakehub(10001)、权限 0700 ---
# 以 root 运行时修正属主（已是正确属主时跳过 chown）；chmod 失败、
# chown 失败都直接报错退出——继续跑只会重蹈 v0.3.0 的重启循环。
# 非 root 运行时（不支持的用法，仅作兼容）跳过属主修正，只尝试 chmod，
# 失败警告不拦截；后续密钥写失败时会由 resolve_secret 报错退出。
if [ "$(id -u)" = "0" ]; then
  _dir_uid="$(stat -c '%u' "$DATA_DIR" 2>/dev/null || echo "")"
  if [ "$_dir_uid" != "10001" ]; then
    mkdir -p "$DATA_DIR"
    if ! chown -R wakehub:wakehub "$DATA_DIR"; then
      echo "[entrypoint] 错误：无法把 $DATA_DIR 属主改成 wakehub(10001)，容器无法启动（请检查挂载点属主与文件系统）" >&2
      exit 1
    fi
  fi
  if ! chmod 700 "$DATA_DIR"; then
    echo "[entrypoint] 错误：无法把 $DATA_DIR 权限收紧为 0700，容器无法启动" >&2
    exit 1
  fi
else
  mkdir -p "$DATA_DIR"
  # 数据目录收紧为 0700（v0.2.9 问题 11）；非 root 时失败只警告不拦截。
  chmod 700 "$DATA_DIR" 2>/dev/null || echo "[entrypoint] 警告：数据目录 $DATA_DIR 收紧为 0700 失败"
fi

# rand_hex <字节数>：输出指定字节数的随机十六进制串。
rand_hex() {
  head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'
}

# rand_password <长度>：输出指定长度的随机字母数字密码。
rand_password() {
  tr -dc 'A-Za-z0-9' < /dev/urandom | head -c "$1"
}

# ensure_owner <文件>：属主不是 wakehub(10001) 时才 chown（已正确时跳过）。
# 以 root 运行时才改属主；chown 失败返回 1（由调用方报错退出）。
ensure_owner() {
  _cur_uid="$(stat -c '%u' "$1" 2>/dev/null || echo "")"
  if [ "$_cur_uid" != "10001" ]; then
    if [ "$(id -u)" = "0" ]; then
      if ! chown wakehub:wakehub "$1"; then
        echo "[entrypoint] 错误：无法把 $1 属主改成 wakehub" >&2
        return 1
      fi
    else
      echo "[entrypoint] 错误：$1 属主不是 wakehub，当前非 root 无法修正" >&2
      return 1
    fi
  fi
}

# secure_secret_file <文件>：密钥文件收紧为属主 wakehub、权限 0600。
# 失败返回 1（由调用方报错退出）。
secure_secret_file() {
  ensure_owner "$1" || return 1
  if ! chmod 600 "$1"; then
    echo "[entrypoint] 错误：无法把 $1 权限收紧为 0600" >&2
    return 1
  fi
}

# resolve_secret <env 名> <文件名> <种类：password|hex> <是否打印：0|1>：
#   解析出的值存进全局变量 RESOLVED_VALUE（调用方读它，不要用 $(...)）。
#   env 有非占位值 → 用 env 值并写回文件；
#   否则文件存在且非空 → 用文件值（并把权限收紧为 0600、属主 wakehub）；
#   否则按种类生成 → 写文件（0600、属主 wakehub）→ 用生成值。
#   任何一步写失败立即 stderr 报错并 exit 1；“已保存”只在写成功后打印。
# 占位值列表与 internal/config 的 Validate() 保持一致。
resolve_secret() {
  _env_name="$1"
  _file="$DATA_DIR/$2"
  _kind="$3"
  _print="$4"
  _env_val="$(printenv "$_env_name" 2>/dev/null || true)"
  case "$_env_val" in
    ""|"wakehub123"|"replace-with-a-strong-admin-password"|"change-me-in-production-wakehub-secret"|"replace-with-strong-secret-key"|"replace-with-random-secret-key"|"local-agent-secret-token"|"replace-with-random-agent-token")
      ;;
    *)
      if ! printf '%s' "$_env_val" > "$_file"; then
        echo "[entrypoint] 错误：无法写入 $_file（请检查 $DATA_DIR 的属主与权限）" >&2
        exit 1
      fi
      secure_secret_file "$_file" || exit 1
      RESOLVED_VALUE="$_env_val"
      return 0
      ;;
  esac
  if [ -s "$_file" ]; then
    secure_secret_file "$_file" || exit 1
    if ! RESOLVED_VALUE="$(cat "$_file")"; then
      echo "[entrypoint] 错误：无法读取 $_file" >&2
      exit 1
    fi
    return 0
  fi
  if [ "$_kind" = "password" ]; then
    _val="$(rand_password 16)"
  else
    _val="$(rand_hex 32)"
  fi
  if [ -z "$_val" ]; then
    echo "[entrypoint] 错误：无法生成 $_env_name 的随机值" >&2
    exit 1
  fi
  if ! printf '%s' "$_val" > "$_file"; then
    echo "[entrypoint] 错误：无法写入 $_file（请检查 $DATA_DIR 的属主与权限）" >&2
    exit 1
  fi
  secure_secret_file "$_file" || exit 1
  if [ "$_print" = "1" ]; then
    echo "[entrypoint] 未设置 $_env_name，已随机生成并保存到数据卷：$_val" >&2
  else
    echo "[entrypoint] 未设置 $_env_name，已随机生成并保存到数据卷（不打印）" >&2
  fi
  RESOLVED_VALUE="$_val"
}

# --- 解析三个密钥（顺序固定：先解析，后导出） ---
resolve_secret WAKEHUB_ADMIN_PASSWORD .admin_password password 1
ADMIN_PASSWORD="$RESOLVED_VALUE"
resolve_secret WAKEHUB_SECRET_KEY .secret_key hex 0
SECRET_KEY="$RESOLVED_VALUE"
resolve_secret WAKEHUB_AGENT_ENROLL_TOKEN .enroll_token hex 0
ENROLL_TOKEN="$RESOLVED_VALUE"

export WAKEHUB_ADMIN_PASSWORD="$ADMIN_PASSWORD"
export WAKEHUB_SECRET_KEY="$SECRET_KEY"
export WAKEHUB_AGENT_ENROLL_TOKEN="$ENROLL_TOKEN"

# --- Server 默认路径（容器内固定值，compose 模式由 .env 覆盖） ---
export WAKEHUB_LISTEN_ADDR="${WAKEHUB_LISTEN_ADDR:-:8080}"
export WAKEHUB_DB_PATH="${WAKEHUB_DB_PATH:-/app/data/wakehub.db}"
export WAKEHUB_MIGRATIONS_PATH="${WAKEHUB_MIGRATIONS_PATH:-/app/migrations/001_init.sql}"
export WAKEHUB_WEB_DIST="${WAKEHUB_WEB_DIST:-/app/web/dist}"

# --- 本地 Agent 默认配置（与 Server 同容器，回环连接） ---
# WS 端口跟随 Server 监听端口（WAKEHUB_LISTEN_ADDR），改端口时不脱节。
_LISTEN_PORT="${WAKEHUB_LISTEN_ADDR##*:}"
_LISTEN_PORT="${_LISTEN_PORT:-8080}"
export WAKEHUB_SERVER_WS_URL="${WAKEHUB_SERVER_WS_URL:-ws://127.0.0.1:${_LISTEN_PORT}/api/v1/ws/agent}"
export WAKEHUB_AGENT_ID="${WAKEHUB_AGENT_ID:-agent-local}"
export WAKEHUB_AGENT_NAME="${WAKEHUB_AGENT_NAME:-Local Agent}"
export WAKEHUB_AGENT_TOKEN="${WAKEHUB_AGENT_TOKEN:-$ENROLL_TOKEN}"
export WAKEHUB_AGENT_TOKEN_FILE="${WAKEHUB_AGENT_TOKEN_FILE:-/app/data/agent-token}"
# 网段不配时 Agent 启动时自动检测本机首个非回环 IPv4 网段（v0.3.0）。

# --- 降权方式：以 root 启动时用 su-exec 降到 wakehub 再跑长驻进程 ---
# su-exec（setuid/setgid 后直接 exec 目标，无中间包装进程）：PID 与
# 信号行为与直接启动一致，环境变量原样透传。非 root 运行时（不支持
# 的用法，仅作兼容）不降权，直接以当前用户启动。
if [ "$(id -u)" = "0" ]; then
  command -v su-exec >/dev/null 2>&1 || { echo "[entrypoint] 错误：未找到 su-exec（镜像构建缺失），无法降权启动" >&2; exit 1; }
  id wakehub >/dev/null 2>&1 || { echo "[entrypoint] 错误：镜像内无 wakehub 用户，无法降权启动" >&2; exit 1; }
  RUN_AS="su-exec wakehub:wakehub"
else
  echo "[entrypoint] 警告：当前非 root 运行，跳过属主修正与降权，Server/Agent 将以当前用户运行" >&2
  RUN_AS=""
fi

echo "[entrypoint] WakeHub 单容器启动：Server 监听 ${WAKEHUB_LISTEN_ADDR}，本地 Agent ID ${WAKEHUB_AGENT_ID}"

# --- 双进程启动与监督 ---
$RUN_AS /app/wakehub-server &
SERVER_PID=$!
$RUN_AS /app/wakehub-agent &
AGENT_PID=$!

cleanup() {
  kill "$SERVER_PID" "$AGENT_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  wait "$AGENT_PID" 2>/dev/null || true
}
trap cleanup TERM INT

# 任一进程退出即结束循环；sleep 会被 trap 打断，属正常。
while kill -0 "$SERVER_PID" 2>/dev/null && kill -0 "$AGENT_PID" 2>/dev/null; do
  sleep 1
done

echo "[entrypoint] 任一进程已退出，正在停止另一个并退出容器"
cleanup
exit 1
