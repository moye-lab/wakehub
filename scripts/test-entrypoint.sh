#!/usr/bin/env bash
# WakeHub entrypoint 桩测（v0.3.1）
#
# 三路场景 + 反向验证（旧版 entrypoint 在同一夹具下必定暴露问题）。
# 用法：bash scripts/test-entrypoint.sh
# 要求：以 root 运行（要建 wakehub 用户、要 chown）；wakehub 用户
# 不存在时自动创建（uid 10001，仅测试用）。桩掉的只有 su-exec
# （用 su -p 真实降权到 wakehub）与 chown（记调用、再调真实 chown）；
# 其余命令（stat/chmod/mkdir/id/cat）全是真实的。
#
# 三路场景（新版 entrypoint，docker-entrypoint.sh）：
#   1. root 属主目录 → chown 成 wakehub 后以 wakehub 启动，密钥落盘属主正确。
#   2. 已正确属主的目录 → 不重复 chown，密钥从文件复用，也能跑。
#   3. 密钥写失败（.secret_key 为悬空符号链接）→ 非零退出、无“已保存”字样、
#      Server/Agent 均未启动。
# 反向验证（旧版 entrypoint，git show HEAD:docker-entrypoint.sh）：
#   R1. 同场景 1 的夹具：旧版不 chown、不降权，目录保持 root 属主，
#       桩进程以 uid 0 运行（证明场景 1 能抓住缺 chown/缺降权的回归）。
#   R2. 同场景 3 的夹具、去掉 set -e（模拟 BusyBox ash 的宽松行为，
#       VM 真机上就是它）：旧版打印虚假的“已保存”并继续启动进程；
#       同法跑新版则仍非零退出且无“已保存”（证明新版的正确性不依赖
#       set -e 的 shell 差异，显式检查才是修根）。

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." >/dev/null 2>&1 && pwd -P)"
ENTRYPOINT="$REPO_ROOT/docker-entrypoint.sh"
[ -f "$ENTRYPOINT" ] || { echo "[test-entrypoint] 找不到 $ENTRYPOINT" >&2; exit 1; }

[ "$(id -u)" = "0" ] || { echo "[test-entrypoint] 请以 root 运行（要建用户、要 chown）" >&2; exit 1; }
if ! id wakehub >/dev/null 2>&1; then
  echo "[test-entrypoint] 创建测试用户 wakehub (uid 10001)……"
  useradd -u 10001 -M -s /bin/sh wakehub || { echo "[test-entrypoint] 创建 wakehub 用户失败" >&2; exit 1; }
fi
REAL_CHOWN="$(command -v chown)"
[ -n "$REAL_CHOWN" ] || { echo "[test-entrypoint] 找不到 chown" >&2; exit 1; }

WORK="$(mktemp -d /tmp/wakehub-entrypoint-test.XXXXXX)"
MARKERS="$WORK/markers"
mkdir -p "$MARKERS"
# 桩进程以 wakehub(10001) 运行，marker 须写进 wakehub 可写的目录。
chown wakehub:wakehub "$MARKERS"
chmod 755 "$WORK" "$MARKERS"
FAKEBIN="$WORK/fakebin"
mkdir -p "$FAKEBIN"
PASS=0
FAIL=0
ENTRY_PID=""
STUB_PIDS=""

ok()   { PASS=$((PASS+1)); echo "  PASS: $*"; }
bad()  { FAIL=$((FAIL+1)); echo "  FAIL: $*" >&2; }
check(){ if eval "$2"; then ok "$1"; else bad "$1"; fi; }

cleanup() {
  if [ -n "$ENTRY_PID" ] && kill -0 "$ENTRY_PID" 2>/dev/null; then
    kill -TERM "$ENTRY_PID" 2>/dev/null || true
    sleep 1
    kill -KILL "$ENTRY_PID" 2>/dev/null || true
  fi
  # 桩进程经 su 包装，entrypoint 退出后可能残留，按记录的 PID 清理。
  for p in $STUB_PIDS; do kill -KILL "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# --- 桩：su-exec（记调用，用 su -p 真实降权到 wakehub 再 exec） ---
cat > "$FAKEBIN/su-exec" <<'EOF'
#!/bin/sh
echo "su-exec $*" >> "$SU_EXEC_LOG"
_u="${1%%:*}"; shift
exec su -p "$_u" -s /bin/sh -c 'exec "$@"' _ "$@"
EOF
chmod 755 "$FAKEBIN/su-exec"

# --- 桩：chown（记调用，再调真实 chown） ---
cat > "$FAKEBIN/chown" <<EOF
#!/bin/sh
echo "chown \$*" >> "\$CHOWN_LOG"
exec $REAL_CHOWN "\$@"
EOF
chmod 755 "$FAKEBIN/chown"

# --- 桩：Server/Agent（记 uid 与拿到的密钥，然后长睡，等测试来杀） ---
make_stub() {
  local name="$1" marker="$2"
  cat > "$WORK/$name" <<EOF
#!/bin/sh
{
  echo "pid=\$\$"
  echo "uid=\$(id -u)"
  echo "ADMIN_PASSWORD=\$WAKEHUB_ADMIN_PASSWORD"
  echo "SECRET_KEY=\$WAKEHUB_SECRET_KEY"
  echo "ENROLL_TOKEN=\$WAKEHUB_AGENT_ENROLL_TOKEN"
  echo "AGENT_TOKEN=\$WAKEHUB_AGENT_TOKEN"
} > "$marker"
sleep 60
EOF
  chmod 755 "$WORK/$name"
}

# 准备一份 entrypoint 副本：二进制路径换成桩。
# $1=源 entrypoint，$2=输出路径，$3=是否去掉 set -eu（反向验证 R2 用）。
prep_entrypoint() {
  cp "$1" "$2"
  sed -i "s|/app/wakehub-server|$WORK/stub-server|g; s|/app/wakehub-agent|$WORK/stub-agent|g" "$2"
  if [ "$3" = "strip-set-e" ]; then
    sed -i '/^set -eu$/d' "$2"
  fi
  chmod 755 "$2"
}

# 启动 entrypoint（后台），$1=entrypoint 路径，$2=data 目录，
# 其余环境变量由调用方事先 export。返回时 ENTRY_PID 为其 PID。
start_entrypoint() {
  local ep="$1" data="$2"
  export WAKEHUB_DATA_DIR="$data"
  export WAKEHUB_DB_PATH="$data/wakehub.db"
  export WAKEHUB_MIGRATIONS_PATH="$REPO_ROOT/migrations/001_init.sql"
  export WAKEHUB_WEB_DIST="$REPO_ROOT/web/dist"
  export WAKEHUB_LISTEN_ADDR=":18098"
  export SU_EXEC_LOG="$WORK/su-exec.log"
  export CHOWN_LOG="$WORK/chown.log"
  : > "$SU_EXEC_LOG"
  : > "$CHOWN_LOG"
  STUB_PIDS=""
  PATH="$FAKEBIN:$PATH" "$ep" > "$WORK/entry.log" 2>&1 &
  ENTRY_PID=$!
}

# 等桩 marker 出现（最多 $1 秒），成功返回 0。
wait_markers() {
  local i
  for i in $(seq 1 "$(( $1 * 5 ))"); do
    if [ -f "$MARKERS/server.marker" ] && [ -f "$MARKERS/agent.marker" ]; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

stop_entrypoint() {
  if [ -n "$ENTRY_PID" ] && kill -0 "$ENTRY_PID" 2>/dev/null; then
    kill -TERM "$ENTRY_PID" 2>/dev/null || true
    local i
    for i in $(seq 1 25); do
      kill -0 "$ENTRY_PID" 2>/dev/null || return 0
      sleep 0.2
    done
    kill -KILL "$ENTRY_PID" 2>/dev/null || true
  fi
  # 桩进程可能残留（经 su 包装），按 marker 里的 PID 清理。
  for m in "$MARKERS/server.marker" "$MARKERS/agent.marker"; do
    if [ -f "$m" ]; then
      local p
      p="$(grep '^pid=' "$m" | cut -d= -f2)"
      [ -n "$p" ] && kill -KILL "$p" 2>/dev/null || true
      STUB_PIDS="$STUB_PIDS $p"
    fi
  done
  wait "$ENTRY_PID" 2>/dev/null || true
  ENTRY_PID=""
}

# ============================================================
echo "== 场景 1：root 属主目录 → chown 后以 wakehub 启动 =="
S1="$WORK/s1"
mkdir -p "$S1/data"   # root 属主，空目录（模拟 VM 上 /root/data）
make_stub stub-server "$MARKERS/server.marker"
make_stub stub-agent "$MARKERS/agent.marker"
prep_entrypoint "$ENTRYPOINT" "$WORK/ep1.sh" keep
rm -f "$MARKERS/server.marker" "$MARKERS/agent.marker"
WAKEHUB_ADMIN_PASSWORD="TestPass123" WAKEHUB_SECRET_KEY="" WAKEHUB_AGENT_ENROLL_TOKEN="" \
  start_entrypoint "$WORK/ep1.sh" "$S1/data"
if wait_markers 10; then
  check "数据目录属主 wakehub" "[ \"\$(stat -c %u \"$S1/data\")\" = \"10001\" ]"
  check "数据目录权限 0700" "[ \"\$(stat -c %a \"$S1/data\")\" = \"700\" ]"
  check "chown 被调用过（修过属主）" "grep -q \"chown.*$S1/data\" \"$WORK/chown.log\""
  for f in .admin_password .secret_key .enroll_token; do
    check "$f 存在且属主 wakehub" "[ -f \"$S1/data/$f\" ] && [ \"\$(stat -c %u \"$S1/data/$f\")\" = \"10001\" ]"
    check "$f 权限 0600" "[ \"\$(stat -c %a \"$S1/data/$f\")\" = \"600\" ]"
  done
  check ".admin_password 内容为 env 值" "[ \"\$(cat \"$S1/data/.admin_password\")\" = \"TestPass123\" ]"
  check "su-exec 对 Server 用 wakehub" "grep -q \"su-exec wakehub:wakehub $WORK/stub-server\" \"$WORK/su-exec.log\""
  check "su-exec 对 Agent 用 wakehub" "grep -q \"su-exec wakehub:wakehub $WORK/stub-agent\" \"$WORK/su-exec.log\""
  check "Server 桩以 uid 10001 运行" "[ \"\$(grep '^uid=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"10001\" ]"
  check "Agent 桩以 uid 10001 运行" "[ \"\$(grep '^uid=' \"$MARKERS/agent.marker\" | cut -d= -f2)\" = \"10001\" ]"
  check "Server 与 Agent 拿到同一 SECRET_KEY" "[ \"\$(grep '^SECRET_KEY=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"\$(grep '^SECRET_KEY=' \"$MARKERS/agent.marker\" | cut -d= -f2)\" ] && [ -n \"\$(grep '^SECRET_KEY=' \"$MARKERS/server.marker\" | cut -d= -f2)\" ]"
  check "Server 与 Agent 拿到同一 ENROLL_TOKEN" "[ \"\$(grep '^ENROLL_TOKEN=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"\$(grep '^ENROLL_TOKEN=' \"$MARKERS/agent.marker\" | cut -d= -f2)\" ] && [ -n \"\$(grep '^ENROLL_TOKEN=' \"$MARKERS/server.marker\" | cut -d= -f2)\" ]"
  check "Server 与文件 .secret_key 一致" "[ \"\$(grep '^SECRET_KEY=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"\$(cat \"$S1/data/.secret_key\")\" ]"
else
  bad "桩进程 10 秒内未启动（见 $WORK/entry.log，测试结束即删）"
  cat "$WORK/entry.log" >&2 || true
fi
stop_entrypoint

# ============================================================
echo "== 场景 2：已正确属主的目录 → 不重复 chown，密钥复用 =="
S2="$WORK/s2"
mkdir -p "$S2/data"
printf '%s' "OldPass456" > "$S2/data/.admin_password"
printf '%s' "oldsecret00000000000000000000000000000000000000000000000000000000" > "$S2/data/.secret_key"
printf '%s' "oldtoken00000000000000000000000000000000000000000000000000000000" > "$S2/data/.enroll_token"
chown -R wakehub:wakehub "$S2/data"
chmod 700 "$S2/data"
chmod 600 "$S2/data"/.admin_password "$S2/data"/.secret_key "$S2/data"/.enroll_token
rm -f "$MARKERS/server.marker" "$MARKERS/agent.marker"
prep_entrypoint "$ENTRYPOINT" "$WORK/ep2.sh" keep
# 不设任何密钥 env：应从文件复用。
WAKEHUB_ADMIN_PASSWORD="" WAKEHUB_SECRET_KEY="" WAKEHUB_AGENT_ENROLL_TOKEN="" \
  start_entrypoint "$WORK/ep2.sh" "$S2/data"
if wait_markers 10; then
  check "未对数据目录重复 chown" "! grep -q \"chown.*$S2/data\" \"$WORK/chown.log\""
  check "未对密钥文件重复 chown" "! grep -q \"chown.*\\.admin_password\\|chown.*\\.secret_key\\|chown.*\\.enroll_token\" \"$WORK/chown.log\""
  check "管理员密码从文件复用" "[ \"\$(grep '^ADMIN_PASSWORD=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"OldPass456\" ]"
  check "SECRET_KEY 从文件复用" "[ \"\$(grep '^SECRET_KEY=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"oldsecret00000000000000000000000000000000000000000000000000000000\" ]"
  check "ENROLL_TOKEN 从文件复用" "[ \"\$(grep '^ENROLL_TOKEN=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"oldtoken00000000000000000000000000000000000000000000000000000000\" ]"
  check "文件内容未被改写" "[ \"\$(cat \"$S2/data/.admin_password\")\" = \"OldPass456\" ] && [ \"\$(cat \"$S2/data/.secret_key\")\" = \"oldsecret00000000000000000000000000000000000000000000000000000000\" ]"
  check "桩进程仍以 uid 10001 运行" "[ \"\$(grep '^uid=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"10001\" ]"
else
  bad "桩进程 10 秒内未启动"
  cat "$WORK/entry.log" >&2 || true
fi
stop_entrypoint

# ============================================================
echo "== 场景 3：密钥写失败 → 非零退出、无“已保存”、进程未启动 =="
S3="$WORK/s3"
mkdir -p "$S3/data"
# .secret_key 为悬空符号链接：[ -s ] 为假（走生成分支），写时打不开。
ln -s /nonexistent-dir-xyz/secret "$S3/data/.secret_key"
rm -f "$MARKERS/server.marker" "$MARKERS/agent.marker"
prep_entrypoint "$ENTRYPOINT" "$WORK/ep3.sh" keep
RC=0
WAKEHUB_ADMIN_PASSWORD="TestPass123" WAKEHUB_SECRET_KEY="" WAKEHUB_AGENT_ENROLL_TOKEN="" \
  PATH="$FAKEBIN:$PATH" WAKEHUB_DATA_DIR="$S3/data" WAKEHUB_DB_PATH="$S3/data/wakehub.db" \
  WAKEHUB_MIGRATIONS_PATH="$REPO_ROOT/migrations/001_init.sql" WAKEHUB_WEB_DIST="$REPO_ROOT/web/dist" \
  WAKEHUB_LISTEN_ADDR=":18098" SU_EXEC_LOG="$WORK/su-exec.log" CHOWN_LOG="$WORK/chown.log" \
  "$WORK/ep3.sh" > "$WORK/entry3.log" 2>&1 || RC=$?
check "写失败时退出码非零（rc=$RC）" "[ \"$RC\" -ne 0 ]"
check "报错提到无法写入" "grep -q \"错误：无法写入\" \"$WORK/entry3.log\""
check "无虚假的“已随机生成并保存”字样" "! grep -q \"已随机生成并保存\" \"$WORK/entry3.log\""
check "Server 桩未启动" "[ ! -f \"$MARKERS/server.marker\" ]"
check "Agent 桩未启动" "[ ! -f \"$MARKERS/agent.marker\" ]"

# ============================================================
echo "== 反向验证 R1：旧版 entrypoint 在场景 1 夹具下不 chown、不降权 =="
OLD_EP="$WORK/old-entrypoint.sh"
git -C "$REPO_ROOT" show HEAD:docker-entrypoint.sh > "$OLD_EP"
R1="$WORK/r1"
mkdir -p "$R1/data"   # root 属主，空目录
rm -f "$MARKERS/server.marker" "$MARKERS/agent.marker"
prep_entrypoint "$OLD_EP" "$WORK/epR1.sh" keep
WAKEHUB_ADMIN_PASSWORD="TestPass123" WAKEHUB_SECRET_KEY="" WAKEHUB_AGENT_ENROLL_TOKEN="" \
  start_entrypoint "$WORK/epR1.sh" "$R1/data"
if wait_markers 10; then
  check "R1：旧版未改目录属主（仍为 root）" "[ \"\$(stat -c %u \"$R1/data\")\" = \"0\" ]"
  check "R1：旧版未调 su-exec" "[ ! -s \"$WORK/su-exec.log\" ]"
  check "R1：旧版桩进程以 uid 0 运行" "[ \"\$(grep '^uid=' \"$MARKERS/server.marker\" | cut -d= -f2)\" = \"0\" ]"
else
  bad "R1：旧版桩进程未启动（反向验证本身失败）"
  cat "$WORK/entry.log" >&2 || true
fi
stop_entrypoint

# ============================================================
echo "== 反向验证 R2：去掉 set -e 后，旧版打印虚假“已保存”，新版仍报错退出 =="
R2="$WORK/r2"
mkdir -p "$R2/data"
ln -s /nonexistent-dir-xyz/secret "$R2/data/.secret_key"
rm -f "$MARKERS/server.marker" "$MARKERS/agent.marker"
# 旧版（去掉 set -eu，模拟 BusyBox ash 的宽松行为）。
prep_entrypoint "$OLD_EP" "$WORK/epR2-old.sh" strip-set-e
WAKEHUB_ADMIN_PASSWORD="TestPass123" WAKEHUB_SECRET_KEY="" WAKEHUB_AGENT_ENROLL_TOKEN="" \
  start_entrypoint "$WORK/epR2-old.sh" "$R2/data"
sleep 3
check "R2：旧版打印了虚假的“已保存”" "grep -q \"已随机生成并保存\" \"$WORK/entry.log\""
check "R2：旧版仍继续启动了桩进程" "[ -f \"$MARKERS/server.marker\" ]"
stop_entrypoint
# 新版（同样去掉 set -eu）：显式检查应使其照样报错退出。
rm -f "$MARKERS/server.marker" "$MARKERS/agent.marker"
prep_entrypoint "$ENTRYPOINT" "$WORK/epR2-new.sh" strip-set-e
RC=0
WAKEHUB_ADMIN_PASSWORD="TestPass123" WAKEHUB_SECRET_KEY="" WAKEHUB_AGENT_ENROLL_TOKEN="" \
  PATH="$FAKEBIN:$PATH" WAKEHUB_DATA_DIR="$R2/data" WAKEHUB_DB_PATH="$R2/data/wakehub.db" \
  WAKEHUB_MIGRATIONS_PATH="$REPO_ROOT/migrations/001_init.sql" WAKEHUB_WEB_DIST="$REPO_ROOT/web/dist" \
  WAKEHUB_LISTEN_ADDR=":18098" SU_EXEC_LOG="$WORK/su-exec.log" CHOWN_LOG="$WORK/chown.log" \
  "$WORK/epR2-new.sh" > "$WORK/entryR2new.log" 2>&1 || RC=$?
check "R2：新版去掉 set -e 后退出码仍非零（rc=$RC）" "[ \"$RC\" -ne 0 ]"
check "R2：新版无虚假的“已随机生成并保存”字样" "! grep -q \"已随机生成并保存\" \"$WORK/entryR2new.log\""
check "R2：新版未启动桩进程" "[ ! -f \"$MARKERS/server.marker\" ]"

echo "----------------------------------------"
echo "[test-entrypoint] PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = "0" ] || exit 1
