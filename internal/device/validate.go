package device

import (
	"errors"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/wakehub/wakehub/internal/auth"
	"github.com/wakehub/wakehub/internal/sleep"
	"github.com/wakehub/wakehub/internal/wol"
)

// ErrDeviceNotFound 表示设备行不存在。
var ErrDeviceNotFound = errors.New("device not found")

// ErrScheduleNotFound 表示定时任务行不存在。
var ErrScheduleNotFound = errors.New("schedule not found")

// ErrMACConflict 表示 MAC 地址已被其他设备占用。
var ErrMACConflict = errors.New("mac address already exists")

// ErrGroupNameConflict 表示分组名称已被占用（契约 §11：冲突 → 409）。
var ErrGroupNameConflict = errors.New("group name already exists")

// ErrAgentNotFound 表示 Agent 行不存在（由 AgentResolver 适配层映射）。
var ErrAgentNotFound = errors.New("agent not found")

// AgentResolver 是设备校验查询 Agent 状态的最小接口。
// 用接口而不是直接依赖 internal/agent，避免 device 包与 agent 包
// 循环引用（agent 已依赖 device）。api 与 scheduler 各自适配。
type AgentResolver interface {
	// LookupAgent 返回 Agent 的审批状态（pending | approved | disabled）
	// 与当前是否在线；Agent 不存在时返回 ErrAgentNotFound。
	LookupAgent(id string) (approvalStatus string, online bool, err error)
}

var (
	validDeviceTypes = map[string]bool{
		"pc": true, "server": true, "nas": true, "raspberry_pi": true,
		"tv_box": true, "router": true, "other": true,
	}
	validStatusMethods = map[string]bool{
		"ping": true, "tcp": true, "arp": true,
	}
	validSleepTypes = map[string]bool{
		"none": true, "http_sol": true, "ssh": true,
	}
)

// ValidationResult 是统一验证层的返回：Errors 为硬性错误（任一存在即
// 拒绝请求），Warnings 为非阻塞警告（如目标 Agent 离线，见契约 §4.2）。
type ValidationResult struct {
	Errors   []auth.FieldError
	Warnings []auth.FieldError
}

// HasErrors 报告是否存在硬性校验错误。
func (v ValidationResult) HasErrors() bool { return len(v.Errors) > 0 }

// ValidateDevice 是契约 §4.2 的统一验证层，新增与修改共用。
// 校验顺序：必填 → 格式/范围/枚举 → 引用存在性（Agent/Group）→ 业务规则
// （Agent 已获批、MAC 唯一）。selfID 为更新时自身的设备 ID（MAC 唯一
// 检查排除自身）；新建时传 ""。调用方须先对 d 做字段合并（更新为部分
// 更新语义，见 router.handleUpdateDevice），本函数只校验合并后的完整行。
// 空的可选字段按“稍后由仓库填默认值”处理，不报错：wol_port/status_port
// 为 0 视为未填；device_type/status_method/sleep_type/group_id 为空视为未填。
func ValidateDevice(r *Repository, resolver AgentResolver, d *Device, selfID string) ValidationResult {
	var res ValidationResult
	addErr := func(field, code, msg string) {
		res.Errors = append(res.Errors, auth.FieldError{Field: field, Code: code, Message: msg})
	}

	// —— 必填 ——
	name := strings.TrimSpace(d.Name)
	if name == "" {
		addErr("name", "required", "设备名称不能为空")
	} else if utf8.RuneCountInString(name) > 64 {
		addErr("name", "out_of_range", "设备名称最长 64 个字符")
	}
	d.Name = name

	mac := strings.TrimSpace(d.MAC)
	if mac == "" {
		addErr("mac", "required", "MAC 地址不能为空")
	} else if normalized, err := wol.ValidateMAC(mac); err != nil {
		addErr("mac", "invalid_format", "MAC 地址格式错误，应为 AA:BB:CC:DD:EE:FF")
	} else {
		d.MAC = normalized
	}

	// —— 格式 / 范围 / 枚举 ——
	if strings.TrimSpace(d.IP) != "" {
		ip := net.ParseIP(strings.TrimSpace(d.IP))
		if ip == nil || ip.To4() == nil {
			addErr("ip", "invalid_format", "IP 地址格式错误，应为合法 IPv4 地址")
		} else {
			d.IP = ip.String()
		}
	}
	if strings.TrimSpace(d.BroadcastAddress) != "" {
		bc := net.ParseIP(strings.TrimSpace(d.BroadcastAddress))
		if bc == nil || bc.To4() == nil {
			addErr("broadcast_address", "invalid_format", "广播地址格式错误，应为合法 IPv4 地址")
		} else {
			d.BroadcastAddress = bc.String()
		}
	}
	if d.WOLPort < 0 || d.WOLPort > 65535 {
		addErr("wol_port", "out_of_range", "WOL 端口范围为 1–65535")
	}
	if d.StatusPort < 0 || d.StatusPort > 65535 {
		addErr("status_port", "out_of_range", "检测端口范围为 1–65535")
	}
	if d.DeviceType != "" && !validDeviceTypes[d.DeviceType] {
		addErr("device_type", "invalid_value", "设备类型不在允许范围内")
	}
	if d.StatusMethod != "" && !validStatusMethods[d.StatusMethod] {
		addErr("status_method", "invalid_value", "状态检测方式须为 ping、tcp 或 arp")
	}
	if d.SleepType != "" && !validSleepTypes[d.SleepType] {
		addErr("sleep_type", "invalid_value", "睡眠适配器类型须为 none、http_sol 或 ssh")
	}
	if utf8.RuneCountInString(d.Remark) > 500 {
		addErr("remark", "out_of_range", "备注最长 500 个字符")
	}

	// 睡眠端点：http_sol 时必填且须符合 SSRF allowlist（契约 §6.1）；
	// none 时若填了端点同样校验（填了就必须是安全的）。
	if d.SleepType == "http_sol" {
		if strings.TrimSpace(d.SleepEndpoint) == "" {
			addErr("sleep_endpoint", "required", "使用 http_sol 适配器时必须提供睡眠端点")
		} else if err := sleep.ValidateEndpoint(d.SleepEndpoint); err != nil {
			addErr("sleep_endpoint", "invalid_value", err.Error())
		}
	} else if strings.TrimSpace(d.SleepEndpoint) != "" {
		if err := sleep.ValidateEndpoint(d.SleepEndpoint); err != nil {
			addErr("sleep_endpoint", "invalid_value", err.Error())
		}
	}

	// SSH 适配器（契约 §6.3）：用户名必填；端口 0 视为未填（用默认 22），
	// 其余须在 1–65535 内；密码以明文入参（SSHPassword）或已存密文
	// （SSHPasswordEnc）二者之一为准——新建时必须给明文，更新时已存
	// 密文在合并后仍在行里，未给明文即视为保持原密码。
	if d.SleepType == "ssh" {
		if strings.TrimSpace(d.SSHUser) == "" {
			addErr("ssh_user", "required", "使用 SSH 适配器时必须提供 SSH 用户名")
		}
		if d.SSHPort < 0 || d.SSHPort > 65535 {
			addErr("ssh_port", "out_of_range", "SSH 端口范围为 1–65535")
		}
		if strings.TrimSpace(d.SSHPassword) == "" && strings.TrimSpace(d.SSHPasswordEnc) == "" {
			addErr("ssh_password", "required", "使用 SSH 适配器时必须提供 SSH 密码")
		}
		// 目标系统：空视为 linux（仓库默认值）；只接受 linux/windows。
		if d.SSHOS != "" && d.SSHOS != "linux" && d.SSHOS != "windows" {
			addErr("ssh_os", "invalid_value", "SSH 目标系统须为 linux 或 windows")
		}
	}

	// —— Agent：必填 → 存在 → 已获批；离线只警告 ——
	agentID := strings.TrimSpace(d.AgentID)
	d.AgentID = agentID
	if agentID == "" {
		addErr("agent_id", "required", "必须选择目标 Agent")
	} else if agentID == "local" {
		// 内置 local 节点已退役（契约 §3.6）：不再是合法值。
		addErr("agent_id", "not_found", "Agent local 不存在")
	} else if resolver != nil {
		approval, online, err := resolver.LookupAgent(agentID)
		if err != nil {
			if errors.Is(err, ErrAgentNotFound) {
				addErr("agent_id", "not_found", "Agent "+agentID+" 不存在")
			} else {
				addErr("agent_id", "invalid_value", "Agent 查询失败："+err.Error())
			}
		} else {
			switch approval {
			case "pending":
				addErr("agent_id", "not_authorized", "Agent "+agentID+" 尚未获批")
			case "disabled":
				addErr("agent_id", "agent_disabled", "Agent "+agentID+" 已禁用")
			case "approved":
				if !online {
					res.Warnings = append(res.Warnings, auth.FieldError{
						Field:   "agent_id",
						Code:    "agent_offline",
						Message: "目标 Agent " + agentID + " 当前离线，设备已创建但暂时无法唤醒",
					})
				}
			default:
				addErr("agent_id", "not_authorized", "Agent "+agentID+" 状态异常："+approval)
			}
		}
	}

	// —— Group 存在性 ——
	groupID := strings.TrimSpace(d.GroupID)
	d.GroupID = groupID
	if groupID != "" {
		exists, err := r.GroupExists(groupID)
		if err != nil {
			addErr("group_id", "invalid_value", "分组查询失败："+err.Error())
		} else if !exists {
			addErr("group_id", "not_found", "分组 "+groupID+" 不存在")
		}
	}

	// —— Network 存在性（v0.2.9 问题 2）：未填时跟随 Agent 的首个管理网络，
	// Agent 无网络时用默认网络（与仓库层 resolveNetworkID 一致）。
	networkID := r.resolveNetworkID(strings.TrimSpace(d.NetworkID), d.AgentID)
	d.NetworkID = networkID
	if exists, err := r.NetworkExists(networkID); err != nil {
		addErr("network_id", "invalid_value", "网络查询失败："+err.Error())
	} else if !exists {
		addErr("network_id", "not_found", "网络 "+networkID+" 不存在")
	}

	// —— Network 归属（v0.3.3 问题 3）：default 网络任何 Agent 可用；
	// 其他网络须由该 Agent 管理，否则拒绝。归属查询失败时 fail-closed。
	if agentID != "" {
		if ok, err := r.AgentManagesNetwork(agentID, networkID); err != nil {
			addErr("network_id", "invalid_value", "网络归属查询失败："+err.Error())
		} else if !ok {
			addErr("network_id", "not_managed", "网络 "+networkID+" 不由 Agent "+agentID+" 管理")
		}
	}

	// —— MAC 唯一（排除自身，网络内）——
	if d.MAC != "" && len(res.Errors) == 0 {
		// 只有前置校验全部通过才查唯一性，避免无意义的 DB 查询；
		// 即使 MAC 本身非法，ValidateMAC 已在上面报错，此处不再重复。
		if other, err := r.GetDeviceByMACInNetwork(d.MAC, d.NetworkID); err == nil && other != nil && other.ID != selfID {
			addErr("mac", "conflict", "MAC 地址已被设备 "+other.Name+" 占用")
		}
	}
	return res
}

// OpCheckError 是契约 §4.4 统一操作校验链的失败描述。
// 单设备操作按 HTTPStatus 返回；批量/定时按 BatchStatus
// （skipped = 未执行，failed = 执行失败）与 Code 记录。
type OpCheckError struct {
	Code        string // reason 码（device_not_found 等）
	Message     string
	HTTPStatus  int    // 单设备操作映射的 HTTP 状态码
	BatchStatus string // "skipped" | "failed"
}

func (e *OpCheckError) Error() string { return e.Message }

// ValidateOperation 执行契约 §4.4 的统一校验链，供单设备唤醒/检测/睡眠、
// 批量唤醒、定时任务执行共用。顺序：设备存在 → 启用 → Agent 存在 →
// 已获批 → 在线。全部通过返回设备行与 nil。
func ValidateOperation(r *Repository, resolver AgentResolver, deviceID string) (*Device, *OpCheckError) {
	d, err := r.GetDevice(deviceID)
	if err != nil {
		if errors.Is(err, ErrDeviceNotFound) {
			return nil, &OpCheckError{Code: "device_not_found", Message: "设备不存在", HTTPStatus: 404, BatchStatus: "skipped"}
		}
		return nil, &OpCheckError{Code: "device_not_found", Message: "设备查询失败", HTTPStatus: 500, BatchStatus: "failed"}
	}
	if !d.IsEnabled() {
		return nil, &OpCheckError{Code: "device_disabled", Message: "设备已禁用", HTTPStatus: 400, BatchStatus: "skipped"}
	}
	agentID := strings.TrimSpace(d.AgentID)
	if agentID == "" || agentID == "local" {
		return nil, &OpCheckError{Code: "agent_not_found", Message: "设备未关联有效 Agent", HTTPStatus: 502, BatchStatus: "failed"}
	}
	if resolver == nil {
		return nil, &OpCheckError{Code: "agent_not_found", Message: "Agent 查询不可用", HTTPStatus: 502, BatchStatus: "failed"}
	}
	approval, online, err := resolver.LookupAgent(agentID)
	if err != nil {
		if errors.Is(err, ErrAgentNotFound) {
			return nil, &OpCheckError{Code: "agent_not_found", Message: "Agent " + agentID + " 不存在", HTTPStatus: 502, BatchStatus: "failed"}
		}
		return nil, &OpCheckError{Code: "agent_not_found", Message: "Agent 查询失败", HTTPStatus: 502, BatchStatus: "failed"}
	}
	switch approval {
	case "pending":
		return nil, &OpCheckError{Code: "agent_not_approved", Message: "Agent " + agentID + " 尚未获批", HTTPStatus: 502, BatchStatus: "failed"}
	case "disabled":
		return nil, &OpCheckError{Code: "agent_disabled", Message: "Agent " + agentID + " 已禁用", HTTPStatus: 502, BatchStatus: "failed"}
	case "approved":
		// 继续走在线检查。
	default:
		return nil, &OpCheckError{Code: "agent_not_approved", Message: "Agent " + agentID + " 状态异常：" + approval, HTTPStatus: 502, BatchStatus: "failed"}
	}
	if !online {
		return nil, &OpCheckError{Code: "agent_offline", Message: "Agent " + agentID + " 当前离线", HTTPStatus: 502, BatchStatus: "failed"}
	}
	return d, nil
}
