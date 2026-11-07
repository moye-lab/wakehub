package agent

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"
)

// 测试用入网 token。
const testEnrollToken = "enroll-test-token-1234567890"

// setupTestDB 建一个应用了 001+002+003 迁移的临时库。
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// 单连接：与生产 InitDB 一致，避免并发写 SQLite 报 database is locked。
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, f := range []string{"001_init.sql", "002_auth.sql", "003_agent.sql"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "migrations", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return db
}

// setupTestHub 建一个测试用 Hub（心跳超时 5s、Ping 间隔 1s）。
func setupTestHub(t *testing.T) *Hub {
	t.Helper()
	hub := NewHubWithStore(NewStore(setupTestDB(t)))
	hub.HeartbeatTimeout = 5 * time.Second
	hub.PingInterval = time.Second
	return hub
}

// newTestServer 起一个最小 WS 服务端：握手走 Hub.AuthenticateAgent，
// 成功后 Upgrade 并进 RegisterConn（与 router.handleAgentWS 同语义）。
// IP 写死 127.0.0.1（回环）；需非回环语义时用 newTestServerWithIP。
func newTestServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	return newTestServerWithIP(t, hub, "127.0.0.1")
}

// newTestServerWithIP 与 newTestServer 同语义，但 IP 由调用方指定
// （v0.3.0：回环自动批准只看 IP，pending 路径测试须用非回环 IP）。
func newTestServerWithIP(t *testing.T, hub *Hub, ip string) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authRes, err := hub.AuthenticateAgent(
			r.Header.Get("X-Agent-Token"), r.Header.Get("X-Agent-ID"),
			r.Header.Get("X-Agent-Name"), r.Header.Get("X-Agent-Subnet"),
			r.Header.Get("X-Agent-Version"), r.Header.Get("X-Agent-OS"),
			r.Header.Get("X-Agent-Arch"), ip, testEnrollToken)
		if err != nil {
			if ae, ok := err.(*AgentAuthError); ok {
				w.WriteHeader(ae.Status)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// 与 router.handleAgentWS 同语义：自动批准时立即下发新 token。
		if authRes.Token != "" {
			raw, _ := json.Marshal(RegistrationApprovedPayload{AgentID: authRes.AgentID, Token: authRes.Token})
			_ = conn.WriteJSON(Message{ID: uuid.NewString(), Type: "registration_approved", Payload: raw})
		}
		hub.RegisterConn(authRes.AgentID, conn, authRes.Approved)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func dialRaw(t *testing.T, url, token, agentID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	h.Set("X-Agent-Token", token)
	h.Set("X-Agent-ID", agentID)
	return websocket.DefaultDialer.Dial(url, h)
}

func dialTestAgent(t *testing.T, url, token, agentID string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dialRaw(t, url, token, agentID)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial agent %s failed: %v (http %d)", agentID, err, status)
	}
	return conn
}

// testAgentConn 是测试用的 Agent 端：可发心跳、可自动回包任务结果，
// 并记录收到的全部服务端消息。
type testAgentConn struct {
	t                 *testing.T
	conn              *websocket.Conn
	received          chan Message
	done              chan struct{}
	stop              chan struct{}
	writeMu           sync.Mutex
	heartbeatInterval time.Duration
	autoReply         bool
}

func connectTestAgent(t *testing.T, url, token, agentID string, heartbeatInterval time.Duration, autoReply bool) *testAgentConn {
	t.Helper()
	ta := &testAgentConn{
		t:                 t,
		conn:              dialTestAgent(t, url, token, agentID),
		received:          make(chan Message, 64),
		done:              make(chan struct{}),
		stop:              make(chan struct{}),
		heartbeatInterval: heartbeatInterval,
		autoReply:         autoReply,
	}
	go ta.run()
	return ta
}

func (ta *testAgentConn) writeJSON(v any) error {
	ta.writeMu.Lock()
	defer ta.writeMu.Unlock()
	_ = ta.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return ta.conn.WriteJSON(v)
}

func (ta *testAgentConn) run() {
	defer close(ta.done)
	if ta.heartbeatInterval > 0 {
		go func() {
			ticker := time.NewTicker(ta.heartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ta.stop:
					return
				case <-ticker.C:
					_ = ta.writeJSON(Message{ID: uuid.NewString(), Type: "heartbeat"})
				}
			}
		}()
	}
	for {
		var msg Message
		if err := ta.conn.ReadJSON(&msg); err != nil {
			return
		}
		select {
		case ta.received <- msg:
		default:
		}
		if ta.autoReply {
			switch msg.Type {
			case "wake", "check", "scan", "sleep":
				raw, _ := json.Marshal(ResultPayload{Success: true})
				_ = ta.writeJSON(Message{ID: msg.ID, Type: "result", Payload: raw})
			}
		}
	}
}

func (ta *testAgentConn) Close() {
	close(ta.stop)
	_ = ta.conn.Close()
	select {
	case <-ta.done:
	case <-time.After(5 * time.Second):
		ta.t.Log("test agent run did not exit in time")
	}
}

// waitFor 在 timeout 内轮询 cond，超时则测试失败。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
