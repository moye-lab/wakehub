#!/usr/bin/env bash
# WakeHub 一键安装脚本（v0.3.2）
# 用法（可重复运行，配置保留、服务重建）：
#   cd wakehub
#   bash install.sh
#
# 部署模式（安装器开头先问）：
#   1 = 本机 docker compose 部署 server + local agent（默认，单网段家用场景）
#   2 = 仅 server（多网段场景的中心端，Agent 另行部署）
#   3 = 仅 agent（本机只跑 Agent，连到另一台上的 server）
#
# 要求：已安装 Docker 与 Docker Compose v2；本脚本不自动安装 Docker。
# 提权口径（D-N4）：请以 docker 组成员身份直接跑 `bash install.sh`，不要 sudo；
# 若 docker info 报权限不足，再把用户加入 docker 组后重新登录，或临时 sudo。
# 以 sudo 运行时，本脚本会把生成的 .env 属主改回原用户，避免后续
# 非 sudo 的 docker compose 读不到 600 权限的 .env。
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"
ENV_FILE="$SCRIPT_DIR/.env"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.yml"

die() { echo "[WakeHub 安装] 错误：$*" >&2; exit 1; }
info() { echo "[WakeHub 安装] $*"; }
warn() { echo "[WakeHub 安装] 警告：$*" >&2; }

# ---------- 0. 预检：docker / compose / curl / 随机数工具 ----------
if ! command -v docker >/dev/null 2>&1; then
  cat >&2 << 'EOF'
[WakeHub 安装] 本机未找到 docker，请先手动安装 Docker 再重跑本脚本：
  Ubuntu/Debian：sudo apt-get update && sudo apt-get install -y docker.io docker-compose-v2 || sudo apt-get install -y docker.io docker-compose-plugin
  （说明：Ubuntu 24.04 源里 Compose v2 的包名是 docker-compose-v2，docker-compose-plugin 是 Docker 官方源的名字，两者是同一组件的不同打包名）
  官方安装指引：https://docs.docker.com/engine/install/
  装好后确认 docker compose version 能输出版本号，再执行 bash install.sh。
EOF
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  cat >&2 << 'EOF'
[WakeHub 安装] docker 已安装但 Docker Compose v2 不可用，请手动安装：
  Ubuntu/Debian：sudo apt-get install -y docker-compose-v2 || sudo apt-get install -y docker-compose-plugin
  然后确认 docker compose version 能输出版本号，再重跑 bash install.sh。
EOF
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  die "当前用户无权访问 Docker（docker info 失败）。请把用户加入 docker 组后重新登录（sudo usermod -aG docker $USER），或临时用 sudo 运行本脚本。"
fi
# curl 是就绪门与登录自检必需的；随机数生成至少需要 od / python3 / openssl 其一。
if ! command -v curl >/dev/null 2>&1; then
  die "本机未找到 curl（就绪检查与登录自检必需）。请先安装 curl 再重跑：sudo apt-get install -y curl"
fi
if ! command -v od >/dev/null 2>&1 && ! command -v python3 >/dev/null 2>&1 && ! command -v openssl >/dev/null 2>&1; then
  die "本机既无 od 也无 python3/openssl，无法生成随机密钥。请先安装其中之一（如 sudo apt-get install -y openssl）再重跑。"
fi

# ---------- 工具函数 ----------
# rand_hex <字节数>：输出指定字节数的十六进制随机串（od → python3 → openssl）。
rand_hex() {
  local n="$1"
  if command -v od >/dev/null 2>&1; then
    head -c "$n" /dev/urandom | od -An -tx1 | tr -d ' \n'
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 -c "import os,sys; print(os.urandom(int(sys.argv[1])).hex())" "$n"
    return 0
  fi
  openssl rand -hex "$n"
}

# load_env_default <KEY> <FALLBACK>：读已有 .env 做默认值（回车保留）。
load_env_default() {
  local key="$1" fallback="$2" val=""
  if [ -f "$ENV_FILE" ]; then
    val="$(grep -E "^${key}=" "$ENV_FILE" | tail -n 1 | cut -d= -f2- || true)"
  fi
  if [ -n "$val" ]; then printf '%s' "$val"; else printf '%s' "$fallback"; fi
}

# valid_cidr <CIDR>：校验 IPv4 私有网段（python3 ipaddress 优先，否则 bash 正则）。
valid_cidr() {
  local cidr="$1"
  if command -v python3 >/dev/null 2>&1; then
    python3 -c "import ipaddress,sys; n=ipaddress.IPv4Network(sys.argv[1], strict=False); sys.exit(0 if n.is_private and 8 <= n.prefixlen <= 32 else 1)" "$cidr" 2>/dev/null
    return $?
  fi
  [[ "$cidr" =~ ^(10|172\.(1[6-9]|2[0-9]|3[01])|192\.168)\.([0-9]{1,3})\.([0-9]{1,3})/([0-9]{1,2})$ ]] || return 1
  return 0
}

# detect_lan_subnet：取默认路由网卡的直连网段 CIDR（v0.2.9 问题 8）。
# 优先 ip route 的直连网段；其次用本机首个全局 IPv4 地址算网段；
# 最后用 hostname -I 的首个 IP 猜 /24。检测不到时输出空——调用方
# 改为必填，不擅自用 192.168.1.0/24 当默认值。
detect_lan_subnet() {
  local iface cidr ip
  if command -v ip >/dev/null 2>&1; then
    iface="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}' | head -1)"
    if [ -n "$iface" ]; then
      cidr="$(ip -4 route show dev "$iface" scope link 2>/dev/null | awk '{print $1}' | grep -E '/' | head -1)"
    fi
    if [ -z "$cidr" ]; then
      ip="$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | head -1)"
      if [ -n "$ip" ] && command -v python3 >/dev/null 2>&1; then
        cidr="$(python3 -c "import ipaddress,sys; print(ipaddress.IPv4Interface(sys.argv[1]).network)" "$ip" 2>/dev/null || true)"
      fi
    fi
  fi
  if [ -z "$cidr" ] && command -v hostname >/dev/null 2>&1; then
    ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
    if [ -n "$ip" ]; then
      cidr="$(printf '%s' "$ip" | awk -F. '{print $1"."$2"."$3".0/24"}')"
    fi
  fi
  if [ -n "$cidr" ] && valid_cidr "$cidr"; then
    printf '%s' "$cidr"
  fi
}

# detect_lan_subnets：枚举本机所有“真实”局域网网段（一行一个 CIDR，
# 默认路由网卡排第一，其余按出现顺序去重）。只取非虚拟网卡（跳过
# docker0/veth*/br-*/flannel/cni/tun/tap/wg/tailscale 等容器与隧道
# 接口）上的全局 IPv4 地址算出的网段——容器网桥（如 docker0 的
# 172.17.0.0/16）不是局域网，不能当 Agent 负责网段。检测不到时输出空。
# v0.2.9 问题 19：多网卡时由主流程编号让用户选，不再只取默认路由网卡。
detect_lan_subnets() {
  local def_iface iface addr cidr seen=""
  if ! command -v ip >/dev/null 2>&1; then return 0; fi
  def_iface="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}' | head -1)"
  while read -r iface addr; do
    case "$iface" in
      docker*|veth*|br-*|flannel*|cni*|tun*|tap*|wg*|tailscale*) continue ;;
    esac
    cidr=""
    if command -v python3 >/dev/null 2>&1; then
      cidr="$(python3 -c "import ipaddress,sys; print(ipaddress.IPv4Interface(sys.argv[1]).network)" "$addr" 2>/dev/null || true)"
    fi
    if [ -z "$cidr" ]; then
      cidr="$(printf '%s' "$addr" | awk -F'[./]' '{print $1"."$2"."$3".0/24"}')"
    fi
    if [ -n "$cidr" ] && valid_cidr "$cidr"; then
      case " $seen " in
        *" $cidr "*) ;;
        *) seen="$seen $cidr"; printf '%s\n' "$cidr" ;;
      esac
    fi
  done <<EOF
$(if [ -n "$def_iface" ]; then ip -4 -o addr show dev "$def_iface" scope global 2>/dev/null | awk '{print $2, $4}'; fi
ip -4 -o addr show scope global 2>/dev/null | awk '{print $2, $4}')
EOF
}

# ask_subnet <标签>：问负责网段；有默认值时回车保留，无默认值时必填。
# 检测不到网段也不擅自用 192.168.1.0/24（v0.2.9 问题 8）。
ask_subnet() {
  local label="$1"
  while true; do
    if [ -n "$AGENT_SUBNET" ]; then
      printf '%s [回车默认：%s，须与本机所在局域网一致]：' "$label" "$AGENT_SUBNET"
    else
      printf '%s（必填，须与本机所在局域网一致，如 192.168.1.0/24）：' "$label"
    fi
    read -r SUB_INPUT || true
    AGENT_SUBNET="${SUB_INPUT:-$AGENT_SUBNET}"
    if valid_cidr "$AGENT_SUBNET"; then
      return 0
    fi
    echo "网段格式错误（须为私有 IPv4 CIDR，如 192.168.1.0/24），请重新输入。"
  done
}

# is_placeholder <VALUE> <占位值...>：值是否等于任一占位值。
is_placeholder() {
  local v="$1"; shift
  local p
  for p in "$@"; do
    if [ "$v" = "$p" ]; then return 0; fi
  done
  return 1
}

mask_secret() {
  local v="$1"
  if [ "${#v}" -le 8 ]; then
    printf '********'
  else
    printf '%s****%s' "${v:0:4}" "${v: -4}"
  fi
}

# ---------- 1. 先问部署模式（模式决定后面问什么） ----------
echo "============================ WakeHub 安装 ============================"
echo "部署模式（WOL 魔术包只在同一广播域生效，跨网段靠 Agent）："
echo "  1) server + local agent（本机一套，单网段家用场景，默认）"
echo "  2) 仅 server（多网段中心端，Agent 另行部署）"
echo "  3) 仅 agent（本机只跑 Agent，连到另一台 server）"
echo "====================================================================="
MODE="$(load_env_default WAKEHUB_INSTALL_MODE 1)"
printf '请选择部署模式 [回车默认：%s]：' "$MODE"
read -r MODE_INPUT || true
MODE="${MODE_INPUT:-$MODE}"
case "$MODE" in
  1|2|3) ;;
  *) die "部署模式只能是 1、2 或 3（输入了：$MODE）" ;;
esac

# ---------- 2. 逐项提问（当场校验格式，错了重问） ----------
PORT="$(load_env_default WAKEHUB_PORT 8080)"
if [ "$MODE" != "3" ]; then
  while true; do
    printf 'Server 对外端口 [回车默认：%s]：' "$PORT"
    read -r PORT_INPUT || true
    PORT="${PORT_INPUT:-$PORT}"
    if [[ "$PORT" =~ ^[0-9]+$ ]] && [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ]; then
      break
    fi
    echo "端口必须是 1-65535 的数字，请重新输入。"
  done
fi

# 模式 3 不问管理员账号/密码（server 在远端，本机只跑 Agent）。
ADMIN_USER=""
ADMIN_PASSWORD=""
ADMIN_PASSWORD_GENERATED=""
if [ "$MODE" != "3" ]; then
  ADMIN_USER="$(load_env_default WAKEHUB_ADMIN_USER admin)"
  printf '管理员账号 [回车默认：%s]：' "$ADMIN_USER"
  read -r ADMIN_INPUT || true
  ADMIN_USER="${ADMIN_INPUT:-$ADMIN_USER}"
  [ -n "$ADMIN_USER" ] || die "管理员账号不能为空"

  # 管理员密码：已有真实密码按回车保留；占位值（wakehub123 等）一律作废；
  # 无密码时留空则随机生成（生成值只显示一次，请立即记下）。
  EXISTING_PW="$(load_env_default WAKEHUB_ADMIN_PASSWORD "")"
  if is_placeholder "$EXISTING_PW" "wakehub123" "replace-with-a-strong-admin-password" ""; then
    EXISTING_PW=""
  fi
  while true; do
    if [ -n "$EXISTING_PW" ]; then
      printf '管理员密码 [回车保留现有密码（%s 位），输入新密码则替换，至少 8 位]：' "${#EXISTING_PW}"
    else
      printf '管理员密码 [留空则随机生成，至少 8 位]：'
    fi
    read -r -s PW_INPUT || true
    echo
    if [ -z "$PW_INPUT" ]; then
      if [ -n "$EXISTING_PW" ]; then
        ADMIN_PASSWORD="$EXISTING_PW"
        break
      fi
      ADMIN_PASSWORD="$(rand_hex 12)"
      ADMIN_PASSWORD_GENERATED=1
      echo "已随机生成管理员密码（仅显示一次，请立即记下）：$ADMIN_PASSWORD"
      break
    fi
    if is_placeholder "$PW_INPUT" "wakehub123" "replace-with-a-strong-admin-password"; then
      echo "该密码是占位值，不能使用，请换一个。"
      continue
    fi
    if [ "${#PW_INPUT}" -ge 8 ]; then
      ADMIN_PASSWORD="$PW_INPUT"
      break
    fi
    echo "密码至少 8 位，请重新输入。"
  done
fi

# 密钥类（仅 server 模式需要；模式 3 不跑 server，不生成也不写）：
# 已有真实值保留；缺失或占位值则随机生成，绝不把占位值写进生产配置。
# 三处占位值（SECRET_KEY / ENROLL_TOKEN / ADMIN_PASSWORD）都在此补齐，
# 否则新版服务端启动时拒绝启动（契约 §2.7）。
if [ "$MODE" != "3" ]; then
  SECRET_KEY="$(load_env_default WAKEHUB_SECRET_KEY "")"
  if [ -z "$SECRET_KEY" ] || is_placeholder "$SECRET_KEY" "change-me-in-production-wakehub-secret" "replace-with-strong-secret-key" "replace-with-random-secret-key"; then
    SECRET_KEY="$(rand_hex 32)"
    info "已随机生成 WAKEHUB_SECRET_KEY（会话签名密钥）"
  fi

  ENROLL_TOKEN="$(load_env_default WAKEHUB_AGENT_ENROLL_TOKEN "")"
  if [ -z "$ENROLL_TOKEN" ] || is_placeholder "$ENROLL_TOKEN" "local-agent-secret-token" "replace-with-random-agent-token"; then
    # 升级路径（迁移文档 §4.3）：旧 .env 里的全局 token 可沿用为入网 token，
    # 已部署的 Agent 重入网时无需改配置；旧值若已泄露请直接换新值。
    OLD_GLOBAL_TOKEN="$(load_env_default WAKEHUB_AGENT_TOKEN "")"
    if [ -n "$OLD_GLOBAL_TOKEN" ] && ! is_placeholder "$OLD_GLOBAL_TOKEN" "local-agent-secret-token" "replace-with-random-agent-token" && [[ "$OLD_GLOBAL_TOKEN" != wha_* ]]; then
      ENROLL_TOKEN="$OLD_GLOBAL_TOKEN"
      info "已沿用旧全局 token 作为入网 token（WAKEHUB_AGENT_ENROLL_TOKEN）"
    else
      ENROLL_TOKEN="$(rand_hex 16)"
      info "已随机生成 WAKEHUB_AGENT_ENROLL_TOKEN（Agent 入网 token）"
    fi
  fi
fi

AGENT_ID="$(load_env_default WAKEHUB_AGENT_ID agent-home-lan)"
AGENT_NAME="$(load_env_default WAKEHUB_AGENT_NAME Home-HostNetwork-Agent)"
# v0.2.9 问题 8：网段默认值改为自动检测（默认路由网卡的直连网段），
# 不再写死 192.168.1.0/24；检测不到时为空，提问时改为必填。
# 已保存的值优先于检测结果（重跑不覆盖用户的选择）。
DETECTED_SUBNET="$(detect_lan_subnet)"
AGENT_SUBNET="$(load_env_default WAKEHUB_AGENT_SUBNET "$DETECTED_SUBNET")"
if [ -z "$(load_env_default WAKEHUB_AGENT_SUBNET "")" ] && [ "$MODE" != "2" ]; then
  # 未保存值且本模式要问网段：多网卡时编号选（v0.2.9 问题 19），
  # 单网段时沿用老行为（只打印一行检测提示，不打断流程）。
  DETECTED_SUBNETS="$(detect_lan_subnets)"
  DETECTED_COUNT="$(printf '%s\n' "$DETECTED_SUBNETS" | grep -c .)"
  if [ "$DETECTED_COUNT" -gt 1 ]; then
    echo "检测到多个局域网段，请选择 Agent 负责的网段："
    i=1
    printf '%s\n' "$DETECTED_SUBNETS" | while read -r c; do
      printf '  %s) %s\n' "$i" "$c"
      i=$((i + 1))
    done
    echo "  0) 手动输入其他网段"
    while true; do
      printf '请选择 [1-%s，0 手动输入，回车默认 1]：' "$DETECTED_COUNT"
      read -r SUB_CHOICE || true
      SUB_CHOICE="${SUB_CHOICE:-1}"
      if [ "$SUB_CHOICE" = "0" ]; then
        AGENT_SUBNET=""; DETECTED_SUBNET=""
        break
      fi
      case "$SUB_CHOICE" in
        # 非数字或超过 3 位一律重问（防超大数字让 -ge 在 set -e 下炸脚本）。
        ''|*[!0-9]*|????*) echo "请输入 0-$DETECTED_COUNT 的数字。"; continue ;;
      esac
      if [ "$SUB_CHOICE" -ge 1 ] && [ "$SUB_CHOICE" -le "$DETECTED_COUNT" ]; then
        AGENT_SUBNET="$(printf '%s\n' "$DETECTED_SUBNETS" | sed -n "${SUB_CHOICE}p")"
        DETECTED_SUBNET="$AGENT_SUBNET"
        break
      fi
      echo "请输入 0-$DETECTED_COUNT 的数字。"
    done
  elif [ -n "$DETECTED_SUBNET" ]; then
    info "检测到本机网段：$DETECTED_SUBNET（默认路由网卡的直连网段），已作为默认值；不对请直接输入正确网段。"
  fi
fi
SERVER_WS_URL="$(load_env_default WAKEHUB_SERVER_WS_URL "ws://127.0.0.1:${PORT}/api/v1/ws/agent")"
# 明文 ws 开关（v0.2.9 问题 3）：模式 3 输了非回环 ws:// 时可改成 1；
# 其余模式保持已有值（默认 0）。
ALLOW_INSECURE_WS="$(load_env_default WAKEHUB_AGENT_ALLOW_INSECURE_WS 0)"
if [ "$MODE" = "3" ]; then
  # 远端 WS 地址：127.0.0.1 默认值在此模式下是错的（指向本机），必须显式输入。
  case "$SERVER_WS_URL" in
    ws://127.0.0.1*|wss://127.0.0.1*) SERVER_WS_URL="" ;;
  esac
  while true; do
    if [ -n "$SERVER_WS_URL" ]; then
      printf 'Server 的 WebSocket 地址 [回车默认：%s]：' "$SERVER_WS_URL"
    else
      printf 'Server 的 WebSocket 地址（如 wss://wake.example.com/api/v1/ws/agent）：'
    fi
    read -r WS_INPUT || true
    SERVER_WS_URL="${WS_INPUT:-$SERVER_WS_URL}"
    case "$SERVER_WS_URL" in
      wss://*) ALLOW_INSECURE_WS=0; break ;;
      ws://*)
        # v0.2.9 问题 3：非回环明文 ws 默认会被 Agent 拒绝启动。
        WS_HOST="${SERVER_WS_URL#*://}"
        WS_HOST="${WS_HOST%%/*}"
        case "$WS_HOST" in
          \[*\]:*) WS_HOST="${WS_HOST%:*}" ;;
          \[*\]) ;;
          *) WS_HOST="${WS_HOST%%:*}" ;;
        esac
        case "$(printf '%s' "$WS_HOST" | tr 'A-Z' 'a-z')" in
          127.*|localhost|[::1]) break ;;
          *)
            echo "注意：$SERVER_WS_URL 是明文 ws://，Agent 默认拒绝用它连非回环地址（token 明文传输）。"
            echo "公网/跨网段部署请改用 wss://（Server 须在 TLS 反向代理之后）；纯局域网 HTTP 部署可显式放行明文。"
            printf '继续使用明文 ws（将写入 WAKEHUB_AGENT_ALLOW_INSECURE_WS=1）？[y/N] '
            read -r WS_YN || true
            case "$WS_YN" in
              y|Y|yes|YES) ALLOW_INSECURE_WS=1; break ;;
              *) echo "请重新输入 Server 地址（建议 wss://）。"; SERVER_WS_URL=""; continue ;;
            esac
            ;;
        esac
        ;;
      *) echo "Server WebSocket 地址必须以 ws:// 或 wss:// 开头，请重新输入。" ;;
    esac
  done
  printf 'Agent 节点 ID [回车默认：%s]：' "$AGENT_ID"
  read -r AID_INPUT || true
  AGENT_ID="${AID_INPUT:-$AGENT_ID}"
  # 模式 3 的 Agent token 就是远端 server 的入网 token（首次注册用）。
  while true; do
    printf '远端 Server 的入网 token（WAKEHUB_AGENT_ENROLL_TOKEN 的值，必填）：'
    read -r -s AGENT_TOKEN_INPUT || true
    echo
    if [ -n "$AGENT_TOKEN_INPUT" ]; then
      AGENT_TOKEN="$AGENT_TOKEN_INPUT"
      break
    fi
    echo "入网 token 不能为空（不知道值请问远端 server 的管理员，或让对方在 Agents 页预签发）。"
  done
  ask_subnet 'Agent 负责网段'
elif [ "$MODE" = "1" ]; then
  printf 'Local Agent 节点 ID [回车默认：%s]：' "$AGENT_ID"
  read -r AID_INPUT || true
  AGENT_ID="${AID_INPUT:-$AGENT_ID}"
  ask_subnet 'Local Agent 负责网段'
fi

# Agent 侧 token（WAKEHUB_AGENT_TOKEN）：首次注册时填入网 token；
# 已是预签发的 wha_ token 时保留（路径 A），不要用入网 token 覆盖。
if [ "$MODE" != "3" ]; then
  EXISTING_AGENT_TOKEN="$(load_env_default WAKEHUB_AGENT_TOKEN "")"
  case "$EXISTING_AGENT_TOKEN" in
    wha_*) AGENT_TOKEN="$EXISTING_AGENT_TOKEN" ;;
    *) AGENT_TOKEN="$ENROLL_TOKEN" ;;
  esac
fi

# ---------- 3. 打码确认页（写配置前确认） ----------
echo
echo "=========================== 确认配置 ==============================="
echo "部署模式        ：$MODE（1=server+local agent，2=仅 server，3=仅 agent）"
if [ "$MODE" != "3" ]; then
  echo "Server 端口      ：$PORT"
fi
if [ "$MODE" = "1" ]; then
  echo "Agent            ：将启动 local agent（host 网络，网段 $AGENT_SUBNET）"
elif [ "$MODE" = "2" ]; then
  echo "Agent            ：不启动（另行部署 Agent 连到本 server）"
elif [ "$MODE" = "3" ]; then
  echo "Agent            ：将启动 agent（连到 $SERVER_WS_URL）"
  echo "Server           ：不启动（使用远端 server）"
fi
if [ "$MODE" != "3" ]; then
  echo "管理员账号        ：$ADMIN_USER"
  echo "管理员密码        ：********（${#ADMIN_PASSWORD} 位，首次登录后请在网页改密）"
  echo "WAKEHUB_SECRET_KEY：$(mask_secret "$SECRET_KEY")"
  echo "入网 token        ：$(mask_secret "$ENROLL_TOKEN")"
fi
echo "Agent token       ：$(mask_secret "$AGENT_TOKEN")（首次注册用；获批后改走 token 文件）"
echo "配置文件          ：$ENV_FILE（.env 已进 .gitignore，不会进仓库）"
echo "====================================================================="
printf '确认写入并启动？[y/回车确认，n 重新输入]：'
read -r CONFIRM || true
CONFIRM="${CONFIRM:-y}"
case "$CONFIRM" in
  y|Y|yes|YES) ;;
  *) info "已取消，未写入任何配置。请重跑 bash install.sh 重新输入。"; exit 0 ;;
esac

# ---------- 4. 预检端口占用（仅 server 模式；无 ss 又无 python3 时告警） ----------
if [ "$MODE" != "3" ]; then
  if command -v ss >/dev/null 2>&1; then
    if ss -ltn 2>/dev/null | grep -q ":${PORT} "; then
      die "端口 $PORT 已被占用，请换一个端口或先停掉占用进程（ss -ltnp | grep :${PORT}），再重跑安装。"
    fi
  elif command -v python3 >/dev/null 2>&1; then
    if python3 -c "import socket,sys; s=socket.socket(); s.settimeout(1); s.connect(('127.0.0.1', int(sys.argv[1]))); s.close()" "$PORT" 2>/dev/null; then
      die "端口 $PORT 已被占用，请换一个端口或先停掉占用进程，再重跑安装。"
    fi
  else
    warn "本机既无 ss 也无 python3，无法检查端口 $PORT 是否被占用；若启动失败请先排查端口占用。"
  fi
fi

# ---------- 5. 写 .env（原子写：先写临时文件再 mv） ----------
TMP_ENV="$ENV_FILE.tmp.$$"
{
  echo "# WakeHub 配置（由 install.sh 生成；含密钥，请勿公开或提交进仓库）"
  echo "WAKEHUB_INSTALL_MODE=$MODE"
  if [ "$MODE" != "3" ]; then
    echo "WAKEHUB_PORT=$PORT"
    # 绑定地址（v0.2.9 问题 18）：默认 0.0.0.0；重跑安装时保留已有值。
    echo "WAKEHUB_BIND_ADDRESS=$(load_env_default WAKEHUB_BIND_ADDRESS 0.0.0.0)"
    echo "WAKEHUB_ADMIN_USER=$ADMIN_USER"
    echo "WAKEHUB_ADMIN_PASSWORD=$ADMIN_PASSWORD"
    echo "WAKEHUB_SECRET_KEY=$SECRET_KEY"
    echo "WAKEHUB_AGENT_ENROLL_TOKEN=$ENROLL_TOKEN"
  else
    # 模式 3 不跑 server：保留已有 server 配置（若有），不写入空值覆盖。
    OLD_PORT="$(load_env_default WAKEHUB_PORT "")"
    [ -n "$OLD_PORT" ] && echo "WAKEHUB_PORT=$OLD_PORT"
    OLD_BIND="$(load_env_default WAKEHUB_BIND_ADDRESS "")"
    [ -n "$OLD_BIND" ] && echo "WAKEHUB_BIND_ADDRESS=$OLD_BIND"
    OLD_USER="$(load_env_default WAKEHUB_ADMIN_USER "")"
    [ -n "$OLD_USER" ] && echo "WAKEHUB_ADMIN_USER=$OLD_USER"
    OLD_PW="$(load_env_default WAKEHUB_ADMIN_PASSWORD "")"
    [ -n "$OLD_PW" ] && echo "WAKEHUB_ADMIN_PASSWORD=$OLD_PW"
    OLD_SK="$(load_env_default WAKEHUB_SECRET_KEY "")"
    [ -n "$OLD_SK" ] && echo "WAKEHUB_SECRET_KEY=$OLD_SK"
    OLD_ENROLL="$(load_env_default WAKEHUB_AGENT_ENROLL_TOKEN "")"
    [ -n "$OLD_ENROLL" ] && echo "WAKEHUB_AGENT_ENROLL_TOKEN=$OLD_ENROLL"
  fi
  echo "WAKEHUB_AGENT_TOKEN=$AGENT_TOKEN"
  echo "WAKEHUB_AGENT_ID=$AGENT_ID"
  echo "WAKEHUB_AGENT_NAME=$AGENT_NAME"
  echo "WAKEHUB_AGENT_SUBNET=$AGENT_SUBNET"
  echo "WAKEHUB_SERVER_WS_URL=$SERVER_WS_URL"
  echo "WAKEHUB_AGENT_ALLOW_INSECURE_WS=$ALLOW_INSECURE_WS"
  # 构建源（v0.2.9 问题 17）：写入 .env 供 compose build.args 取用，
  # 重跑安装时保留已有值；默认与 Dockerfile 的 ARG 默认值一致。
  echo "NPM_REGISTRY=$(load_env_default NPM_REGISTRY https://registry.npmmirror.com)"
  echo "GOPROXY=$(load_env_default GOPROXY https://goproxy.cn,direct)"
  # 周期状态检测间隔（秒，V-N2）：默认 300；0 为禁用；1–29 钳制为 30。
  echo "WAKEHUB_STATUS_CHECK_INTERVAL=$(load_env_default WAKEHUB_STATUS_CHECK_INTERVAL 300)"
} > "$TMP_ENV"
chmod 600 "$TMP_ENV"
mv "$TMP_ENV" "$ENV_FILE"
# sudo 运行时把 .env 属主还给原用户，否则后续非 sudo 的 compose 读不到它（D-N4）。
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  chown "$SUDO_USER" "$ENV_FILE" 2>/dev/null || warn "无法把 .env 属主改回 $SUDO_USER，后续请用 sudo 跑 docker compose 或手动 chown。"
fi
info "配置已写入 $ENV_FILE"

# ---------- 6. 模式切换：先停旧服务，再启动新模式的服务 ----------
cd "$SCRIPT_DIR" || die "无法进入 $SCRIPT_DIR"
case "$MODE" in
  1) SERVICES="wakehub-server wakehub-agent-local" ;;
  2) SERVICES="wakehub-server" ;;
  3) SERVICES="wakehub-agent-local" ;;
esac
info "正在停止旧服务（模式切换时先停，避免新旧并存）..."
docker compose stop 2>/dev/null || true

# ---------- 7. 启动服务（带重试的拉起；失败即退出，不留半吊子状态） ----------
run_with_retry() {
  local tries=3 n=1
  while [ "$n" -le "$tries" ]; do
    if "$@"; then return 0; fi
    echo "[WakeHub 安装] 第 $n/$tries 次尝试失败，5 秒后重试：$*" >&2
    n=$((n + 1))
    sleep 5
  done
  return 1
}

info "正在构建并启动：$SERVICES"
if ! run_with_retry docker compose up -d --build $SERVICES; then
  echo "[WakeHub 安装] docker compose 启动失败，服务日志：" >&2
  docker compose logs --tail=50 $SERVICES >&2 || true
  exit 1
fi

# ---------- 8. 就绪门：轮询健康接口 + 登录自检（不要只调 GET /） ----------
COOKIE_JAR="$(mktemp)"
trap 'rm -f "$COOKIE_JAR"' EXIT

if [ "$MODE" != "3" ]; then
  info "等待 Server 就绪（轮询 http://127.0.0.1:${PORT}/api/v1/health ，最长 90 秒）..."
  wait_ready() {
    local deadline=$((SECONDS + 90)) code=""
    while [ "$SECONDS" -lt "$deadline" ]; do
      code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:${PORT}/api/v1/health" || true)"
      if [ "$code" = "200" ]; then return 0; fi
      sleep 2
    done
    return 1
  }
  if ! wait_ready; then
    echo "[WakeHub 安装] Server 在 90 秒内未就绪，服务日志：" >&2
    docker compose logs --tail=80 wakehub-server >&2 || true
    exit 1
  fi
  # 再验一次真实登录：能拿到 code 0 才算装好（同时把会话 Cookie 存下，
  # 供模式 1 的自动审批复用）。
  LOGIN_RESP="$(curl -s --max-time 5 -c "$COOKIE_JAR" -X POST "http://127.0.0.1:${PORT}/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" || true)"
  case "$LOGIN_RESP" in
    *'"code":0'*)
      info "登录自检通过。"
      ;;
    *)
      echo "[WakeHub 安装] Server 已响应但登录自检失败（返回：$LOGIN_RESP），服务日志：" >&2
      docker compose logs --tail=80 wakehub-server >&2 || true
      exit 1
      ;;
  esac

  # 模式 1：自动审批本机 local agent（路径 B 自助注册进 pending 后由安装器
  # 代批一次；失败不阻塞安装，改由管理员在 Agents 页手动审批）。
  if [ "$MODE" = "1" ]; then
    if command -v python3 >/dev/null 2>&1; then
      info "正在审批 Local Agent（首次注册需管理员通过一次）..."
      APPROVED=0
      deadline=$((SECONDS + 60))
      while [ "$SECONDS" -lt "$deadline" ]; do
        AGENTS_JSON="$(curl -s --max-time 5 -b "$COOKIE_JAR" "http://127.0.0.1:${PORT}/api/v1/agents" || true)"
        AGENT_STATUS="$(printf '%s' "$AGENTS_JSON" | python3 -c "import json,sys; d=json.load(sys.stdin).get('data',[]); a=[x for x in d if x.get('id')=='$AGENT_ID']; print(a[0].get('status','') if a else 'missing')" 2>/dev/null || true)"
        case "$AGENT_STATUS" in
          pending)
            if curl -s --max-time 5 -b "$COOKIE_JAR" -X POST "http://127.0.0.1:${PORT}/api/v1/agents/${AGENT_ID}/approve" | grep -q '"code":0'; then
              info "Local Agent 已获批，正在重连（新 token 自动持久化到数据卷）。"
              APPROVED=1
            else
              warn "Local Agent 审批接口调用失败，请在网页 Agents 页手动审批。"
            fi
            break
            ;;
          connected|approved)
            info "Local Agent 已连接（此前已获批，无需再次审批）。"
            APPROVED=1
            break
            ;;
          missing|disabled|"")
            sleep 3
            ;;
          *)
            sleep 3
            ;;
        esac
      done
      if [ "$APPROVED" != "1" ]; then
        warn "60 秒内未完成 Local Agent 审批：请打开网页 Agents 页，找到 $AGENT_ID 点『通过』，Agent 获批后自动重连。"
      fi
    else
      warn "本机无 python3，无法自动审批 Local Agent：请打开网页 Agents 页，找到 $AGENT_ID 点『通过』，Agent 获批后自动重连。"
    fi
  fi
else
  info "等待 Agent 连接 Server（最长 90 秒）..."
  AGENT_CONNECTED=0
  deadline=$((SECONDS + 90))
  while [ "$SECONDS" -lt "$deadline" ]; do
    AGENT_LOG="$(docker compose logs --tail=30 wakehub-agent-local 2>/dev/null || true)"
    if printf '%s' "$AGENT_LOG" | grep -q "已成功连接中心端"; then
      AGENT_CONNECTED=1
      break
    fi
    sleep 3
  done
  if [ "$AGENT_CONNECTED" = "1" ]; then
    info "Agent 已成功连接中心端。"
    if docker compose logs --tail=30 wakehub-agent-local 2>/dev/null | grep -q "注册待审批"; then
      warn "Agent 正在等待远端管理员审批：请让远端管理员在网页 Agents 页找到 $AGENT_ID 点『通过』，获批后 Agent 自动重连。"
    fi
  else
    echo "[WakeHub 安装] Agent 在 90 秒内未连上 Server，Agent 日志：" >&2
    docker compose logs --tail=80 wakehub-agent-local >&2 || true
    exit 1
  fi
fi

echo
echo "=========================== 安装完成 ==============================="
if [ "$MODE" != "3" ]; then
  echo "访问地址：http://服务器IP:${PORT}/"
  echo "管理员账号：$ADMIN_USER（密码为安装时设置的密码；忘记密码可用下面的重置命令）"
  echo "忘记密码时：cd $SCRIPT_DIR && docker compose exec wakehub-server /app/wakehub-server reset-password"
fi
if [ "$MODE" = "1" ]; then
  echo "Local Agent：已启动，负责网段 $AGENT_SUBNET"
elif [ "$MODE" = "3" ]; then
  echo "Agent：已连接到 $SERVER_WS_URL"
fi
echo "查看日志：cd $SCRIPT_DIR && docker compose logs -f"
echo "停止服务：cd $SCRIPT_DIR && docker compose down（数据保留在卷里）"
echo "删除全部数据：cd $SCRIPT_DIR && docker compose down -v（删库删 token，执行前先备份：bash scripts/backup.sh backup）"
echo "备份数据：cd $SCRIPT_DIR && bash scripts/backup.sh backup"
echo "再次安装/改配置：cd $SCRIPT_DIR && bash install.sh（已有配置按回车保留）"
echo "====================================================================="
