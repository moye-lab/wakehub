package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestGenerateTokenFormat(t *testing.T) {
	plain, hash := GenerateToken()
	if !strings.HasPrefix(plain, "wha_") {
		t.Fatalf("token 应以 wha_ 开头，实际 %q", plain)
	}
	if len(plain) != 47 {
		t.Fatalf("token 长度应为 47，实际 %d (%q)", len(plain), plain)
	}
	if len(hash) != 64 {
		t.Fatalf("SHA-256 hex 应为 64 字符，实际 %d", len(hash))
	}
	plain2, _ := GenerateToken()
	if plain == plain2 {
		t.Fatalf("两次生成的 token 不应相同")
	}
}

func TestIssueAndVerifyToken(t *testing.T) {
	store := NewStore(setupTestDB(t))
	if _, err := store.CreateAgentApproved("agent-a", "A", "192.168.1.0/24"); err != nil {
		t.Fatalf("CreateAgentApproved: %v", err)
	}
	token, err := store.IssueToken("agent-a")
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	id, revoked, err := store.VerifyTokenHash(token)
	if err != nil {
		t.Fatalf("VerifyTokenHash: %v", err)
	}
	if id != "agent-a" || revoked {
		t.Fatalf("校验结果错误：id=%q revoked=%v", id, revoked)
	}
	if _, _, err := store.VerifyTokenHash("wha_" + strings.Repeat("x", 43)); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("未知 token 应返回 ErrTokenNotFound，实际 %v", err)
	}
	// 重新签发即轮换：旧 token 立即失效。
	token2, err := store.IssueToken("agent-a")
	if err != nil {
		t.Fatalf("reissue: %v", err)
	}
	if token2 == token {
		t.Fatalf("重新签发应生成不同 token")
	}
	if _, _, err := store.VerifyTokenHash(token); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("旧 token 在轮换后应失效，实际 err=%v", err)
	}
	if id, _, err := store.VerifyTokenHash(token2); err != nil || id != "agent-a" {
		t.Fatalf("新 token 应可校验：id=%q err=%v", id, err)
	}
}

func TestRevokeTokenIsolation(t *testing.T) {
	store := NewStore(setupTestDB(t))
	for _, id := range []string{"agent-a", "agent-b"} {
		if _, err := store.CreateAgentApproved(id, id, ""); err != nil {
			t.Fatalf("CreateAgentApproved %s: %v", id, err)
		}
	}
	tokenA, err := store.IssueToken("agent-a")
	if err != nil {
		t.Fatalf("IssueToken A: %v", err)
	}
	tokenB, err := store.IssueToken("agent-b")
	if err != nil {
		t.Fatalf("IssueToken B: %v", err)
	}
	// 吊销 A 的 token。
	if err := store.RevokeToken("agent-a"); err != nil {
		t.Fatalf("RevokeToken A: %v", err)
	}
	if _, revoked, err := store.VerifyTokenHash(tokenA); err != nil || !revoked {
		t.Fatalf("A 的 token 应被标记吊销：revoked=%v err=%v", revoked, err)
	}
	// B 不受影响。
	if id, revoked, err := store.VerifyTokenHash(tokenB); err != nil || id != "agent-b" || revoked {
		t.Fatalf("B 的 token 应仍有效：id=%q revoked=%v err=%v", id, revoked, err)
	}
	// 吊销不存在的 Agent 的 token。
	if err := store.RevokeToken("agent-nope"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("吊销无 token 的 Agent 应返回 ErrTokenNotFound，实际 %v", err)
	}
}

func TestCreateAgentPendingAndApprove(t *testing.T) {
	store := NewStore(setupTestDB(t))
	a, err := store.CreateAgentPending("agent-p", "Pending", "192.168.1.0/24", "0.2.0", "linux", "amd64", "192.168.1.65")
	if err != nil {
		t.Fatalf("CreateAgentPending: %v", err)
	}
	if a.ApprovalStatus != "pending" || a.Authorized {
		t.Fatalf("新建 pending 行状态错误：%+v", a)
	}
	if len(a.AllowedSubnets) != 1 || a.AllowedSubnets[0] != "192.168.1.0/24" {
		t.Fatalf("注册网段应自动进白名单：%v", a.AllowedSubnets)
	}
	// 审批：事务内改状态 + 签发 token。
	token, err := store.ApproveAgent("agent-p")
	if err != nil {
		t.Fatalf("ApproveAgent: %v", err)
	}
	a, err = store.GetAgent("agent-p")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if a.ApprovalStatus != "approved" || !a.Authorized {
		t.Fatalf("审批后状态错误：%+v", a)
	}
	if id, _, err := store.VerifyTokenHash(token); err != nil || id != "agent-p" {
		t.Fatalf("审批签发的 token 应可校验：id=%q err=%v", id, err)
	}
	// 非 pending 不能再审批。
	if _, err := store.ApproveAgent("agent-p"); !errors.Is(err, ErrAgentNotPending) {
		t.Fatalf("重复审批应返回 ErrAgentNotPending，实际 %v", err)
	}
	// 不存在的 Agent 审批。
	if _, err := store.ApproveAgent("agent-nope"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("审批不存在的 Agent 应返回 ErrAgentNotFound，实际 %v", err)
	}
}

func TestCreateAgentApprovedWithToken(t *testing.T) {
	store := NewStore(setupTestDB(t))
	a, token, err := store.CreateAgentApprovedWithToken("agent-x", "X", "192.168.1.0/24")
	if err != nil {
		t.Fatalf("CreateAgentApprovedWithToken: %v", err)
	}
	if a.ApprovalStatus != "approved" || !a.Authorized {
		t.Fatalf("预签发行应直接 approved：%+v", a)
	}
	if id, _, err := store.VerifyTokenHash(token); err != nil || id != "agent-x" {
		t.Fatalf("预签发 token 应可校验：id=%q err=%v", id, err)
	}
}

func TestAgentIDValidation(t *testing.T) {
	if !ValidAgentID("agent-home-lan") || !ValidAgentID("a1") {
		t.Fatalf("合法 ID 应通过")
	}
	for _, bad := range []string{"", "local-x!", "-abc", "a b", strings.Repeat("a", 65)} {
		if ValidAgentID(bad) {
			t.Fatalf("非法 ID %q 应被拒绝", bad)
		}
	}
	if !ReservedAgentID("local") || ReservedAgentID("agent-home-lan") {
		t.Fatalf("只有 local 是保留 ID")
	}
}

func TestAllowedSubnets(t *testing.T) {
	store := NewStore(setupTestDB(t))
	if _, err := store.CreateAgentApproved("agent-s", "S", ""); err != nil {
		t.Fatalf("CreateAgentApproved: %v", err)
	}
	if err := store.AddAllowedSubnet("agent-s", "192.168.1.0/24"); err != nil {
		t.Fatalf("AddAllowedSubnet: %v", err)
	}
	if err := store.AddAllowedSubnet("agent-s", "10.0.1.0/24"); err != nil {
		t.Fatalf("AddAllowedSubnet 2: %v", err)
	}
	if err := store.AddAllowedSubnet("agent-s", "192.168.1.0/24"); !errors.Is(err, ErrSubnetExists) {
		t.Fatalf("重复添加应返回 ErrSubnetExists，实际 %v", err)
	}
	subnets, err := store.ListAllowedSubnets("agent-s")
	if err != nil || len(subnets) != 2 {
		t.Fatalf("白名单应有 2 条：%v err=%v", subnets, err)
	}
	removed, err := store.RemoveAllowedSubnet("agent-s", "10.0.1.0/24")
	if err != nil || !removed {
		t.Fatalf("删除已存在网段应返回 true：removed=%v err=%v", removed, err)
	}
	removed, err = store.RemoveAllowedSubnet("agent-s", "10.0.1.0/24")
	if err != nil || removed {
		t.Fatalf("删除不存在网段应返回 false：removed=%v err=%v", removed, err)
	}
	// 删除 Agent 行时白名单 FK 级联删除。
	if err := store.DeleteAgent("agent-s"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	subnets, err = store.ListAllowedSubnets("agent-s")
	if err != nil || len(subnets) != 0 {
		t.Fatalf("Agent 删除后白名单应为空：%v err=%v", subnets, err)
	}
}

func TestMigration003DeletesLocalSeed(t *testing.T) {
	db := setupTestDB(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM agents WHERE id = 'local'`).Scan(&n); err != nil {
		t.Fatalf("query local: %v", err)
	}
	if n != 0 {
		t.Fatalf("迁移 003 应删除种子 local 行，实际残留 %d 行", n)
	}
	// 003 新增的列与表应存在。
	var col int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('agents') WHERE name = 'approval_status'`).Scan(&col); err != nil || col != 1 {
		t.Fatalf("agents.approval_status 列缺失：col=%d err=%v", col, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'agent_allowed_subnets'`).Scan(&col); err != nil || col != 1 {
		t.Fatalf("agent_allowed_subnets 表缺失：col=%d err=%v", col, err)
	}
}

func TestSetApprovalStatusSyncsAuthorized(t *testing.T) {
	store := NewStore(setupTestDB(t))
	if _, err := store.CreateAgentApproved("agent-d", "D", ""); err != nil {
		t.Fatalf("CreateAgentApproved: %v", err)
	}
	if err := store.SetApprovalStatus("agent-d", "disabled"); err != nil {
		t.Fatalf("SetApprovalStatus: %v", err)
	}
	a, err := store.GetAgent("agent-d")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if a.ApprovalStatus != "disabled" || a.Authorized {
		t.Fatalf("禁用后 authorized 镜像应为 false：%+v", a)
	}
	if err := store.SetApprovalStatus("agent-nope", "approved"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("不存在的 Agent 应返回 ErrAgentNotFound，实际 %v", err)
	}
}
