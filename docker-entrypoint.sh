#!/bin/sh
# WakeHub 单容器入口（v0.3.0）：同一容器内同时运行 Server 与本地 Agent。
#
# 用法（唯一推荐的单容器部署）：
#   docker run -d --name=wakehub --restart unless-stopped --network host \
#     --cap-add NET_RAW \
#     -e WAKEHUB_ADMIN_PASSWORD=你的密码 \
#     -v ./data:/app/data moyelab/wakehub:0.3.0
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
# 进程监督：Server 与 Agent 双双后台启动，任一退出即杀掉另一个、
# 整个容器退出（--restart unless-stopped 会重拉整个容器）。
# SIGTERM/SIGINT 转发给两个子进程。
#
# 注意：本脚本只在“默认入口”（单容器模式）下运行；docker-compose
# 的 server/agent 两个服务会显式覆盖 entrypoint，各自只跑一个进程，
# 不走本脚本。

set -eu

DATA_DIR="${WAKEHUB_DATA_DIR:-/app/data}"
mkdir -p "$DATA_DIR"
# 数据目录收紧为 0700（v0.2.9 问题 11）；失败只警告不拦截。
chmod 700 "$DATA_DIR" 2>/dev/null || echo "[entrypoint] 警告：数据目录 $DATA_DIR 收紧为 0700 失败"

# rand_hex <字节数>：输出指定字节数的随机十六进制串。
rand_hex() {
  head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'
}

# rand_password <长度>：输出指定长度的随机字母数字密码。
rand_password() {
  tr -dc 'A-Za-z0-9' < /dev/urandom | head -c "$1"
}

# resolve_secret <env 名> <文件名> <种类：password|hex> <是否打印：0|1>：
#   返回值走 stdout（调用方用 $(...) 捕获），日志一律走 stderr，
#   否则日志会混进返回值。
#   env 有非占位值 → 用 env 值并写回文件；
#   否则文件存在 → 用文件值；
#   否则按种类生成 → 写文件（0600）→ 用生成值。
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
      printf '%s' "$_env_val" > "$_file"
      chmod 600 "$_file"
      printf '%s' "$_env_val"
      return 0
      ;;
  esac
  if [ -s "$_file" ]; then
    cat "$_file"
    return 0
  fi
  if [ "$_kind" = "password" ]; then
    _val="$(rand_password 16)"
  else
    _val="$(rand_hex 32)"
  fi
  printf '%s' "$_val" > "$_file"
  chmod 600 "$_file"
  if [ "$_print" = "1" ]; then
    echo "[entrypoint] 未设置 $_env_name，已随机生成并保存到数据卷：$_val" >&2
  else
    echo "[entrypoint] 未设置 $_env_name，已随机生成并保存到数据卷（不打印）" >&2
  fi
  printf '%s' "$_val"
}

# --- 解析三个密钥（顺序固定：先解析，后导出） ---
ADMIN_PASSWORD="$(resolve_secret WAKEHUB_ADMIN_PASSWORD .admin_password password 1)"
SECRET_KEY="$(resolve_secret WAKEHUB_SECRET_KEY .secret_key hex 0)"
ENROLL_TOKEN="$(resolve_secret WAKEHUB_AGENT_ENROLL_TOKEN .enroll_token hex 0)"

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

echo "[entrypoint] WakeHub 单容器启动：Server 监听 ${WAKEHUB_LISTEN_ADDR}，本地 Agent ID ${WAKEHUB_AGENT_ID}"

# --- 双进程启动与监督 ---
/app/wakehub-server &
SERVER_PID=$!
/app/wakehub-agent &
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
