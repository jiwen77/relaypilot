package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSelfUpdateResultTextReportsAlreadyCurrent(t *testing.T) {
	text := selfUpdateResultText("latest", "==> 已是最新版本：v0.1.10\n==> 如需重新安装当前版本，请添加 --force。\n")
	if !strings.Contains(text, "RelayPilot 已是最新版本") || !strings.Contains(text, "当前版本：v0.1.10") || !strings.Contains(text, "无需更新") {
		t.Fatalf("skip text should not claim update: %s", text)
	}
	for _, bad := range []string{"RelayPilot 已更新", "RelayPilot updated to", "==>", "--force"} {
		if strings.Contains(text, bad) {
			t.Fatalf("skip text should be clean already-current wording, contains %q in: %s", bad, text)
		}
	}
}

func TestHubUpdateResultTextReportsAlreadyCurrentCleanly(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	if err := saveHubRegistry(state, defaultHubRegistry()); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "relaypilot")
	writeExecutable(t, cli, "#!/usr/bin/env sh\nprintf '==> 已是最新版本：v0.1.13\\n==> 如需重新安装当前版本，请添加 --force。\\n'\n")
	t.Setenv("RELAYPILOT_CLI", cli)

	text, err := hubUpdateCommandWithDefaults(state, nil, []string{"hub", "latest", "--restart"}, "/relaypilot_uphub", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"Hub 已更新", "版本：latest", "==>", "--force", "12 秒后重启"} {
		if strings.Contains(text, bad) {
			t.Fatalf("already-current update text should be user-facing, not raw/successful update wording; contains %q in:\n%s", bad, text)
		}
	}
	for _, want := range []string{"✅ Hub 已是最新版本", "当前版本：v0.1.13", "无需更新", "未安排重启"} {
		if !strings.Contains(text, want) {
			t.Fatalf("already-current update text missing %q:\n%s", want, text)
		}
	}
}

func TestHubAgentPollSignedFlow(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	agentState := filepath.Join(root, "agent")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds":  []any{obj{"type": "vless", "tag": "vless-in", "users": []any{obj{"name": "hk", "uuid": "u"}}}},
		"outbounds": []any{obj{"type": "direct", "tag": "direct"}},
		"route":     obj{"rules": []any{}, "final": "direct"},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	regFile := filepath.Join(root, "transit.registration.json")
	if err := writeJSON(regFile, obj{
		"kind":         agentRegistrationKind,
		"version":      version,
		"id":           "transit-hk",
		"role":         "transit",
		"name":         "HK Transit",
		"transport":    "poll",
		"labels":       obj{"region": "hk"},
		"capabilities": []any{"status", "doctor", "endpoints", "inspect_conf"},
		"topology":     obj{},
		"health":       obj{},
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importAgentRegistration(hubState, regFile); err != nil {
		t.Fatal(err)
	}
	issued, err := issueHubAgentToken(hubState, "transit-hk", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	if issued["token"] != "test-token" {
		t.Fatalf("unexpected token: %#v", issued["token"])
	}
	queued, err := hubDispatchCommand(hubState, "/status transit-hk")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queued, "批次：") {
		t.Fatalf("dispatch did not include batch: %s", queued)
	}

	srv := httptest.NewServer(&hubServer{stateDir: hubState, quiet: true})
	defer srv.Close()
	res, err := agentPollOnce(srv.URL, "transit-hk", "test-token", "transit", agentState, conf, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res["leased"] != 1 {
		t.Fatalf("leased = %#v", res["leased"])
	}
	tasks, err := listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0]["status"] != "done" {
		t.Fatalf("tasks = %#v", tasks)
	}
	agents, err := listHubAgents(hubState)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := findHubAgentByID(agents, "transit-hk")
	if err != nil {
		t.Fatal(err)
	}
	if len(asObj(agent["topology"])) != 0 {
		t.Fatalf("heartbeat should not refresh topology by default: %#v", agent["topology"])
	}
	if got := formatHubTaskResultsText(tasks, ""); !strings.Contains(got, "transit-hk") || !strings.Contains(got, "endpoints:") {
		t.Fatalf("bad results text: %s", got)
	}

	pathQ := "/api/agents/transit-hk/tasks?limit=1"
	ts := strconvNow()
	nonce := "fixed-replay-nonce"
	sig, err := signAgentRequest("test-token", "GET", pathQ, nil, ts, nonce, "transit-hk")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", srv.URL+pathQ, nil)
	req.Header.Set("X-Agent-Id", "transit-hk")
	req.Header.Set("X-Agent-Timestamp", ts)
	req.Header.Set("X-Agent-Nonce", nonce)
	req.Header.Set("X-Agent-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first signed request status = %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", srv.URL+pathQ, nil)
	req.Header.Set("X-Agent-Id", "transit-hk")
	req.Header.Set("X-Agent-Timestamp", ts)
	req.Header.Set("X-Agent-Nonce", nonce)
	req.Header.Set("X-Agent-Signature", sig)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", resp.StatusCode)
	}
}

func TestAgentPollerReloadsEnrollmentFileWhenChanged(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "agent")
	conf := filepath.Join(root, "conf")
	enrollment := filepath.Join(state, "agent-enrollment.json")
	tokenA := filepath.Join(state, "token-a")
	tokenB := filepath.Join(state, "token-b")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenA, []byte("token-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenB, []byte("token-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(enrollment, obj{
		"hub_url":                    "https://hub-a.example",
		"agent_id":                   "agent-a",
		"role":                       "transit",
		"token_file":                 tokenA,
		"ip_mode":                    "static",
		"public_ip_interval_seconds": 600,
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	poller, stamp, err := newAgentPollerFromEnrollmentFile(enrollment, state, conf, 5, 2*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if poller.agentID != "agent-a" || poller.token != "token-a" || poller.ipMode != "static" || poller.publicIPInterval != 600*time.Second {
		t.Fatalf("initial poller = %#v", poller)
	}
	if err := writeJSON(enrollment, obj{
		"hub_url":                    "https://hub-b.example",
		"agent_id":                   "agent-b",
		"role":                       "landing",
		"token_file":                 tokenB,
		"ip_mode":                    "dynamic",
		"public_ip_interval_seconds": 30,
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	nextMod := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(enrollment, nextMod, nextMod); err != nil {
		t.Fatal(err)
	}
	poller, stamp, changed, err := reloadAgentPollerFromEnrollmentFileIfChanged(enrollment, stamp, poller, state, conf, 5, 2*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected enrollment reload")
	}
	if stamp.Size <= 0 {
		t.Fatalf("stamp not updated: %#v", stamp)
	}
	if poller.agentID != "agent-b" || poller.token != "token-b" || poller.role != "landing" || poller.ipMode != "dynamic" || poller.publicIPInterval != 30*time.Second {
		t.Fatalf("reloaded poller = %#v", poller)
	}
}

func TestHubLinkTransitLandingQueuesBindEndpointTask(t *testing.T) {
	t.Setenv("RELAYPILOT_DISABLE_DATAPLANE_APPLY", "1")
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	landingState := filepath.Join(root, "landing")
	transitState := filepath.Join(root, "transit")
	conf := filepath.Join(root, "conf")
	reality, err := ensureTransitReality(conf, transitState, "::", 443, "vless-in", "www.cloudflare.com", "www.cloudflare.com", 443, "", "0123456789abcdef", "1m", false)
	if err != nil {
		t.Fatal(err)
	}
	if str(reality["public_key"]) == "" || str(reality["short_id"]) != "0123456789abcdef" {
		t.Fatalf("reality = %#v", reality)
	}
	endpoint := obj{
		"kind":        endpointKind,
		"version":     version,
		"name":        "jp",
		"protocol":    "shadowsocks",
		"server":      "203.0.113.20",
		"server_port": 2443,
		"method":      "2022-blake3-aes-128-gcm",
		"password":    "MDEyMzQ1Njc4OWFiY2RlZg==",
		"tag":         "landing-jp-ss",
		"network":     "tcp,udp",
	}
	if err := writeJSON(filepath.Join(landingState, "endpoints", "jp.json"), endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
		},
		"landing-jp": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-jp",
			"role":      "landing",
			"name":      "JP Landing",
			"transport": "poll",
			"topology": obj{"endpoints": []any{
				obj{"name": "jp", "protocol": "shadowsocks", "server": "203.0.113.20", "server_port": 2443, "tag": "landing-jp-ss"},
			}},
		},
	}
	if err := saveHubRegistry(hubState, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := issueHubAgentToken(hubState, "transit-la", "test-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := issueHubAgentToken(hubState, "landing-jp", "landing-token"); err != nil {
		t.Fatal(err)
	}
	queued, err := hubDispatchCommand(hubState, "/link transit-la landing-jp user-jp --inbound-tag vless-in")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queued, "已下发串联任务") || !strings.Contains(queued, "transit-la → landing-jp") {
		t.Fatalf("queued = %s", queued)
	}
	tasks, err := listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || str(tasks[0]["command"]) != "export_endpoint" || str(tasks[0]["agent_id"]) != "landing-jp" {
		t.Fatalf("tasks = %#v", tasks)
	}

	srv := httptest.NewServer(&hubServer{stateDir: hubState, quiet: true})
	defer srv.Close()
	landingRes, err := agentPollOnce(srv.URL, "landing-jp", "landing-token", "landing", landingState, filepath.Join(root, "landing-conf"), 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if landingRes["leased"] != 1 {
		t.Fatalf("landing leased = %#v", landingRes["leased"])
	}
	tasks, err = listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected export + bind tasks, got %#v", tasks)
	}
	var bindQueued bool
	for _, task := range tasks {
		if str(task["command"]) == "bind_endpoint" && str(task["agent_id"]) == "transit-la" && str(task["status"]) == "queued" {
			bindQueued = true
		}
		if str(task["command"]) == "export_endpoint" && str(asObj(task["result"])["password"]) != "" {
			t.Fatalf("export result leaked endpoint password: %#v", task)
		}
	}
	if !bindQueued {
		t.Fatalf("bind task was not queued after landing export: %#v", tasks)
	}
	res, err := agentPollOnce(srv.URL, "transit-la", "test-token", "transit", transitState, conf, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res["leased"] != 1 {
		t.Fatalf("leased = %#v", res["leased"])
	}
	tasks, err = listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks after poll = %#v", tasks)
	}
	var bindTask obj
	for _, task := range tasks {
		if str(task["command"]) == "bind_endpoint" {
			bindTask = task
		}
	}
	if len(bindTask) == 0 || bindTask["status"] != "done" {
		t.Fatalf("bind task after poll = %#v", tasks)
	}
	result := asObj(bindTask["result"])
	if result["success"] != true || str(result["command"]) != "bind_endpoint" || str(result["endpoint_name"]) != "jp" {
		t.Fatalf("task result = %#v", result)
	}
	if str(asObj(asObj(bindTask["payload"])["endpoint"])["password"]) != "" {
		t.Fatalf("completed bind task retained endpoint password: %#v", bindTask)
	}
	if _, err := os.Stat(filepath.Join(transitState, "endpoints", "jp.json")); err != nil {
		t.Fatalf("endpoint not imported to transit state: %v", err)
	}
	summary, err := inspectConfig(conf)
	if err != nil {
		t.Fatal(err)
	}
	authRoutes := asList(asObj(summary["route"])["auth_routes"])
	if len(authRoutes) != 1 || !listContains(asObj(authRoutes[0])["auth_user"], "user-jp") || str(asObj(authRoutes[0])["outbound"]) != "landing-jp-ss" {
		t.Fatalf("auth routes = %#v", authRoutes)
	}
}

func TestHubProbeLinkQueuesSingleLowResourceTask(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
			"topology": obj{"links": []any{
				obj{
					"auth_user":     "jp",
					"endpoint_name": "jp",
					"outbound_tag":  "landing-jp-ss",
					"server":        "127.0.0.1",
					"server_port":   2443,
				},
			}},
		},
		"landing-jp": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-jp",
			"role":      "landing",
			"name":      "JP Landing",
			"transport": "poll",
			"topology": obj{"endpoints": []any{
				obj{"name": "jp", "protocol": "shadowsocks", "server": "198.51.100.20", "server_port": 2443, "tag": "landing-jp-ss"},
			}},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	queued, err := hubDispatchCommand(state, "/probe transit-la landing-jp jp --timeout 30")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queued, "链路检测") || !strings.Contains(queued, "transit-la → landing-jp") || !strings.Contains(queued, "超时：5s") || !strings.Contains(queued, "不会启动后台监控") {
		t.Fatalf("queued = %s", queued)
	}
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || str(tasks[0]["command"]) != "probe_link" || str(tasks[0]["agent_id"]) != "transit-la" {
		t.Fatalf("tasks = %#v", tasks)
	}
	payload := asObj(tasks[0]["payload"])
	if str(payload["host"]) != "127.0.0.1" || int64Value(payload["port"]) != 2443 || int64Value(payload["timeout_seconds"]) != 5 {
		t.Fatalf("payload = %#v", payload)
	}
	if str(payload["landing_id"]) != "landing-jp" || str(payload["endpoint_name"]) != "jp" {
		t.Fatalf("payload link fields = %#v", payload)
	}
}

func TestFormatHubTaskResultsUsesRecentOperationsAndFailureHints(t *testing.T) {
	tasks := []obj{
		{
			"status":     "failed",
			"agent_role": "transit",
			"agent_id":   "transit-la",
			"command":    "bind_endpoint",
			"result": obj{
				"success": false,
				"error":   "sing-box check failed: listen tcp :443: bind: address already in use",
			},
		},
	}
	text := formatHubTaskResultsText(tasks, "")
	if !strings.Contains(text, "最近操作") || strings.Contains(text, "任务结果") {
		t.Fatalf("result title should be user-facing: %s", text)
	}
	if !strings.Contains(text, "建议：检查端口占用") {
		t.Fatalf("missing failure hint: %s", text)
	}
}

func TestHubDecommissionQueuesDryRunAndRequiresExactConfirm(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	preview, err := hubDispatchCommand(state, "/decommission transit-la --mode uninstall")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "退役预览") || !strings.Contains(preview, "节点本机授权") {
		t.Fatalf("preview = %s", preview)
	}
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || str(tasks[0]["command"]) != "decommission_agent" || str(tasks[0]["agent_id"]) != "transit-la" {
		t.Fatalf("tasks = %#v", tasks)
	}
	payload := asObj(tasks[0]["payload"])
	if payload["dry_run"] != true || str(payload["mode"]) != "uninstall" {
		t.Fatalf("payload = %#v", payload)
	}
	if _, err := hubDispatchCommand(state, "/decommission transit-la --mode uninstall --confirm landing-jp"); err == nil {
		t.Fatal("expected exact confirm to be required")
	}
	confirmed, err := hubDispatchCommand(state, "/decommission transit-la --mode uninstall --confirm transit-la")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(confirmed, "已下发远程退役") || !strings.Contains(confirmed, "transit-la") {
		t.Fatalf("confirmed = %s", confirmed)
	}
	tasks, err = listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	payload = asObj(tasks[0]["payload"])
	for _, task := range tasks {
		if str(task["origin_text"]) == "/decommission transit-la --mode uninstall --confirm transit-la" {
			payload = asObj(task["payload"])
			break
		}
	}
	if payload["dry_run"] != false || str(payload["mode"]) != "uninstall" {
		t.Fatalf("confirmed payload = %#v", payload)
	}
}

func TestHubViewCachesReturnCopiesAndInvalidate(t *testing.T) {
	state := t.TempDir()
	nowTs := now()
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-a": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-a",
			"role":      "transit",
			"name":      "Transit A",
			"last_seen": nowTs,
			"topology": obj{"links": []any{
				obj{"auth_user": "user-a", "endpoint_name": "a", "outbound_tag": "landing-a-ss"},
			}},
		},
		"landing-a": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-a",
			"role":      "landing",
			"name":      "Landing A",
			"last_seen": nowTs,
			"topology": obj{"endpoints": []any{
				obj{"name": "a", "tag": "landing-a-ss"},
			}},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	agents, err := listHubAgents(state)
	if err != nil {
		t.Fatal(err)
	}
	agents[0]["name"] = "mutated"
	agentsAgain, err := listHubAgents(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agentsAgain {
		if str(agent["name"]) == "mutated" {
			t.Fatalf("agent cache returned mutable data: %#v", agentsAgain)
		}
	}
	topology, err := cachedHubTopologyText(state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(topology, "landing-a") {
		t.Fatalf("topology = %s", topology)
	}
	related, err := cachedRelatedAgentsForAgent(state, "transit-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 1 || str(related[0].agent["id"]) != "landing-a" {
		t.Fatalf("related = %#v", related)
	}
	reg["agents"] = obj{
		"transit-a": asObj(asObj(reg["agents"])["transit-a"]),
		"landing-b": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-b",
			"role":      "landing",
			"name":      "Landing B",
			"last_seen": nowTs,
			"labels":    obj{"transit": "transit-a"},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	topology, err = cachedHubTopologyText(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(topology, "landing-a") || !strings.Contains(topology, "landing-b") {
		t.Fatalf("stale topology cache = %s", topology)
	}
	related, err = cachedRelatedAgentsForAgent(state, "transit-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 1 || str(related[0].agent["id"]) != "landing-b" {
		t.Fatalf("stale related cache = %#v", related)
	}
	task, err := createHubTask(state, asObj(asObj(reg["agents"])["transit-a"]), "status", nil, "batch-a", "test")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	tasks[0]["status"] = "done"
	tasksAgain, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if str(tasksAgain[0]["status"]) != "queued" {
		t.Fatalf("task cache returned mutable data: %#v", tasksAgain)
	}
	if _, err := completeHubTask(state, str(task["id"]), "transit-a", obj{"success": true, "text": "ok"}); err != nil {
		t.Fatal(err)
	}
	tasksAgain, err = listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if str(tasksAgain[0]["status"]) != "done" {
		t.Fatalf("stale task cache: %#v", tasksAgain)
	}
}

func TestHubViewCachesExpireByMaxAge(t *testing.T) {
	state := t.TempDir()
	nowTs := now()
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-age": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-age",
			"role":      "transit",
			"name":      "Transit Age",
			"last_seen": nowTs,
			"topology": obj{"links": []any{
				obj{"endpoint_name": "age", "outbound_tag": "landing-age-ss"},
			}},
		},
		"landing-age": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-age",
			"role":      "landing",
			"name":      "Landing Age",
			"last_seen": nowTs,
			"topology": obj{"endpoints": []any{
				obj{"name": "age", "tag": "landing-age-ss"},
			}},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := listHubAgents(state); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedHubTopologyText(state); err != nil {
		t.Fatal(err)
	}
	hubViewCache.Lock()
	agentEntry := hubViewCache.agents[state]
	agentEntry.agents[0]["name"] = "Expired Agent Cache"
	agentEntry.cachedAt = time.Now().Add(-hubAgentCacheMaxAge - time.Second)
	hubViewCache.agents[state] = agentEntry
	topologyEntry := hubViewCache.topology[state]
	topologyEntry.topologyText = "Expired Topology Cache"
	topologyEntry.cachedAt = time.Now().Add(-hubTopologyCacheMaxAge - time.Second)
	hubViewCache.topology[state] = topologyEntry
	hubViewCache.Unlock()
	agents, err := listHubAgents(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		if str(agent["name"]) == "Expired Agent Cache" {
			t.Fatalf("expired agent cache reused: %#v", agents)
		}
	}
	topology, err := cachedHubTopologyText(state)
	if err != nil {
		t.Fatal(err)
	}
	if topology == "Expired Topology Cache" || !strings.Contains(topology, "landing-age") {
		t.Fatalf("expired topology cache reused: %s", topology)
	}
	task, err := createHubTask(state, asObj(asObj(reg["agents"])["transit-age"]), "status", nil, "batch-age", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listHubTasks(state); err != nil {
		t.Fatal(err)
	}
	hubViewCache.Lock()
	taskEntry := hubViewCache.tasks[state]
	taskEntry.tasks[0]["status"] = "failed"
	taskEntry.cachedAt = time.Now().Add(-hubTaskCacheMaxAge - time.Second)
	hubViewCache.tasks[state] = taskEntry
	hubViewCache.Unlock()
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if str(tasks[0]["status"]) != "queued" || str(tasks[0]["id"]) != str(task["id"]) {
		t.Fatalf("expired task cache reused: %#v", tasks)
	}
}

func TestHubLinkQueuesDetailRefreshWhenEndpointSnapshotMissing(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
			"topology":  obj{"links": []any{}, "endpoints": []any{}, "inbounds": []any{}},
		},
		"landing-jp": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-jp",
			"role":      "landing",
			"name":      "JP Landing",
			"transport": "poll",
			"topology":  obj{"links": []any{}, "endpoints": []any{}, "inbounds": []any{}},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	queued, err := hubDispatchCommand(state, "/link transit-la landing-jp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queued, "需要刷新节点详情") || !strings.Contains(queued, "已下发节点详情刷新") || !strings.Contains(queued, "刷新完成后再次串联") {
		t.Fatalf("queued = %s", queued)
	}
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected transit+landing refresh tasks, got %#v", tasks)
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		if str(task["command"]) != "sync_agent" {
			t.Fatalf("expected sync_agent task, got %#v", task)
		}
		seen[str(task["agent_id"])] = true
	}
	if !seen["transit-la"] || !seen["landing-jp"] {
		t.Fatalf("refresh tasks should target both link endpoints: %#v", tasks)
	}
}

func TestExecuteProbeLinkTaskReportsReachableTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
		close(accepted)
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	result := executeTask(obj{
		"command": "probe_link",
		"payload": obj{
			"host":            host,
			"port":            port,
			"timeout_seconds": 1,
			"landing_id":      "landing-jp",
			"endpoint_name":   "jp",
		},
	}, "", "")
	if result["success"] != true || result["reachable"] != true || str(result["target"]) != net.JoinHostPort(host, portText) {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := result["duration_ms"]; !ok || !strings.Contains(str(result["text"]), "连通") {
		t.Fatalf("result text/duration = %#v", result)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("probe did not reach listener")
	}
}

func TestExecuteProbeLinkTaskCapsTimeoutAndReportsUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result := executeTask(obj{
		"command": "probe_link",
		"payload": obj{
			"host":            host,
			"port":            port,
			"timeout_seconds": 999,
			"landing_id":      "landing-jp",
			"endpoint_name":   "jp",
		},
	}, "", "")
	if result["success"] != false || result["reachable"] != false || int64Value(result["timeout_seconds"]) != 5 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(str(result["text"]), "不可达") || str(result["error"]) == "" {
		t.Fatalf("result text/error = %#v", result)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("closed local-port probe should fail quickly, took %s; result=%#v", elapsed, result)
	}
}

func TestExecuteDecommissionAgentTaskRequiresLocalPolicy(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(conf, "90-relaypilot-outbounds.json")
	user := filepath.Join(conf, "user.json")
	if err := os.WriteFile(managed, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := executeTask(obj{
		"command": "decommission_agent",
		"payload": obj{"mode": "uninstall", "dry_run": true},
	}, state, conf)
	if result["success"] != false || !strings.Contains(str(result["error"]), "allow_remote_decommission") {
		t.Fatalf("default-deny result = %#v", result)
	}
	if _, err := os.Stat(managed); err != nil {
		t.Fatalf("dry denied task should not remove managed config: %v", err)
	}

	if err := writeJSON(filepath.Join(state, "agent-policy.json"), obj{"allow_remote_decommission": true}, 0o600); err != nil {
		t.Fatal(err)
	}
	result = executeTask(obj{
		"command": "decommission_agent",
		"payload": obj{"mode": "uninstall", "dry_run": true},
	}, state, conf)
	if result["success"] != true || result["dry_run"] != true || str(result["mode"]) != "uninstall" {
		t.Fatalf("preview result = %#v", result)
	}
	text := str(result["text"])
	if !strings.Contains(text, "远程退役预览") || !strings.Contains(text, "90-relaypilot-outbounds.json") || strings.Contains(text, "user.json") {
		t.Fatalf("preview text = %s", text)
	}
	if _, err := os.Stat(managed); err != nil {
		t.Fatalf("preview should not remove managed config: %v", err)
	}
}

func TestExecuteDecommissionAgentTaskRunsConfirmedCleanupViaLocalCLI(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(state, "agent-policy.json"), obj{"allow_remote_decommission": true}, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "decommission.log")
	cli := filepath.Join(root, "relaypilot")
	writeExecutable(t, cli, "#!/usr/bin/env sh\nprintf 'STATE_DIR=%s\\nCONF_DIR=%s\\nARGS=%s\\n' \"$STATE_DIR\" \"$CONF_DIR\" \"$*\" > \"$RELAYPILOT_TEST_COMMAND_LOG\"\n")
	t.Setenv("RELAYPILOT_CLI", cli)
	t.Setenv("RELAYPILOT_TEST_COMMAND_LOG", logPath)
	t.Setenv("RELAYPILOT_DECOMMISSION_EXEC_MODE", "foreground")

	result := executeTask(obj{
		"command": "decommission_agent",
		"payload": obj{"mode": "uninstall", "dry_run": false, "delay_seconds": 1},
	}, state, conf)
	if result["success"] != true || result["dry_run"] != false || str(result["mode"]) != "uninstall" {
		t.Fatalf("confirmed result = %#v", result)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "STATE_DIR="+state) || !strings.Contains(logText, "CONF_DIR="+conf) {
		t.Fatalf("cleanup env missing: %s", logText)
	}
	if !strings.Contains(logText, "ARGS=uninstall --full --purge-proxy-config --yes") {
		t.Fatalf("cleanup args missing: %s", logText)
	}
}

func TestTelegramProbeHelpListsDetectedLinksWithoutProbing(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
			"topology": obj{"links": []any{
				obj{"auth_user": "jp", "endpoint_name": "jp", "outbound_tag": "landing-jp-ss", "server": "127.0.0.1", "server_port": 2443},
			}},
		},
		"landing-jp": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-jp",
			"role":      "landing",
			"name":      "JP Landing",
			"transport": "poll",
			"topology": obj{"endpoints": []any{
				obj{"name": "jp", "protocol": "shadowsocks", "server": "127.0.0.1", "server_port": 2443, "tag": "landing-jp-ss"},
			}},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	reply := handleTelegramHubReply(state, obj{"update_id": 9, "callback_query": obj{"data": "rp:probe_help", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(reply.Text, "可检测链路") || !strings.Contains(reply.Text, "/relaypilot_probe transit-la landing-jp jp") {
		t.Fatalf("probe help should list copyable detected links: %#v", reply)
	}
	if !strings.Contains(reply.Text, "仅生成命令，不会立即探测") {
		t.Fatalf("probe help should make resource behavior explicit: %#v", reply)
	}
}

func TestTelegramCallbackReplyEditsSourceMessage(t *testing.T) {
	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer srv.Close()

	cfg := obj{"api_base": srv.URL, "bot_token": "token", "chat_id": "123", "enabled": true}
	update := obj{"callback_query": obj{"message": obj{"message_id": 42, "chat": obj{"id": "123"}}}}
	reply := telegramReply{
		Text:        "控制卡片",
		ParseMode:   "HTML",
		ReplyMarkup: tgKeyboard([]any{tgButton("返回", "rp:panel")}),
	}

	if err := sendTelegramReplyForUpdate(cfg, update, reply, time.Second); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bottoken/editMessageText" {
		t.Fatalf("callback replies should edit the source card, path=%s body=%s", gotPath, gotBody)
	}
	form, err := url.ParseQuery(gotBody)
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("chat_id") != "123" || form.Get("message_id") != "42" || form.Get("text") != "控制卡片" || form.Get("parse_mode") != "HTML" {
		t.Fatalf("edit payload = %s", gotBody)
	}
	if !strings.Contains(form.Get("reply_markup"), "rp:panel") {
		t.Fatalf("edit payload missing inline keyboard: %s", gotBody)
	}
}

func TestTelegramCallbackPendingBatchKeepsMessageTarget(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{"transit-hk": obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        "transit-hk",
		"role":      "transit",
		"name":      "HK Transit",
		"transport": "poll",
		"last_seen": now(),
	}}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}

	reply := handleTelegramHubReply(state, obj{"update_id": 30, "callback_query": obj{
		"data": "rp:upd:run:transit-hk",
		"message": obj{
			"message_id": 77,
			"chat":       obj{"id": "999"},
		},
	}})
	batchID := extractBatchID(reply.Text)
	if batchID == "" {
		t.Fatalf("update callback should queue a tracked batch: %#v", reply)
	}
	pending, err := loadPendingTGBatches(state)
	if err != nil {
		t.Fatal(err)
	}
	batch := asObj(asObj(pending["batches"])[batchID])
	if batch["chat_id"] != "999" || int64Value(batch["message_id"]) != 77 {
		t.Fatalf("callback batch should keep edit target: %#v", batch)
	}
}

func TestTelegramNavigationCallbacksAreSilent(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{"transit-hk": obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        "transit-hk",
		"role":      "transit",
		"name":      "HK Transit",
		"transport": "poll",
		"last_seen": now(),
	}}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}

	reply := handleTelegramHubReply(state, obj{"update_id": 31, "callback_query": obj{
		"data":    "rp:agents",
		"message": obj{"message_id": 42, "chat": obj{"id": "999"}},
	}})
	if reply.CallbackText != "" {
		t.Fatalf("navigation callbacks should not show toast text: %#v", reply)
	}
	if len(reply.ReplyMarkup) == 0 {
		t.Fatalf("navigation callback should still return inline keyboard: %#v", reply)
	}

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	cfg := obj{"api_base": srv.URL, "bot_token": "token", "chat_id": "123", "enabled": true}
	if err := answerTelegramCallback(cfg, "callback-id", "", time.Second); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "text=") {
		t.Fatalf("silent callback acknowledgement should omit visible text: %s", gotBody)
	}
	if !strings.Contains(gotBody, "callback_query_id=callback-id") {
		t.Fatalf("callback acknowledgement missing id: %s", gotBody)
	}
}

func TestTelegramRestartNoticeSendsOnceAndClears(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	oldBuildVersion := buildVersion
	buildVersion = "v-test"
	t.Cleanup(func() { buildVersion = oldBuildVersion })
	if err := recordPendingTGRestartNotice(state, "latest", 5); err != nil {
		t.Fatal(err)
	}

	var requests int
	var gotPath string
	var gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(data))
		gotText = form.Get("text")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer srv.Close()

	cfg := obj{"api_base": srv.URL, "bot_token": "token", "chat_id": "123", "enabled": true}
	done, err := sendPendingTGRestartNotice(state, cfg, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !done || requests != 1 || gotPath != "/bottoken/sendMessage" {
		t.Fatalf("notice send = done %v requests %d path %s", done, requests, gotPath)
	}
	for _, want := range []string{"✅ Hub/Bot 已重启完成", "当前版本：v-test", "更新目标：latest", "安排时间："} {
		if !strings.Contains(gotText, want) {
			t.Fatalf("restart notice missing %q:\n%s", want, gotText)
		}
	}
	if _, err := os.Stat(telegramRestartNoticePath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("notice should be removed after successful send: %v", err)
	}

	done, err = sendPendingTGRestartNotice(state, cfg, time.Second)
	if err != nil || !done || requests != 1 {
		t.Fatalf("second notice send should be no-op: done=%v err=%v requests=%d", done, err, requests)
	}
}

func TestTelegramRestartNoticeSendFailureKeepsNotice(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	if err := recordPendingTGRestartNotice(state, "latest", 5); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"description":"temporary failure"}`, http.StatusBadGateway)
	}))
	defer srv.Close()

	cfg := obj{"api_base": srv.URL, "bot_token": "token", "chat_id": "123", "enabled": true}
	done, err := sendPendingTGRestartNotice(state, cfg, time.Second)
	if err == nil || done {
		t.Fatalf("failed send should return not done with error: done=%v err=%v", done, err)
	}
	if _, err := os.Stat(telegramRestartNoticePath(state)); err != nil {
		t.Fatalf("notice should remain after failed send: %v", err)
	}
}

func TestExecuteBindEndpointTaskChecksRestartsAndReportsDataPlaneApply(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00-inbounds.json"), obj{"inbounds": []any{obj{"type": "vless", "tag": "vless-in", "users": []any{}}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "01-outbounds.json"), obj{"outbounds": []any{obj{"type": "direct", "tag": "direct"}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "02-route.json"), obj{"route": obj{"rules": []any{}, "final": "direct"}}, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "commands.log")
	binDir := filepath.Join(root, "bin")
	writeExecutable(t, filepath.Join(binDir, "sing-box"), "#!/usr/bin/env sh\nprintf 'sing-box %s\\n' \"$*\" >> \"$RELAYPILOT_TEST_COMMAND_LOG\"\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "systemctl"), "#!/usr/bin/env sh\nprintf 'systemctl %s\\n' \"$*\" >> \"$RELAYPILOT_TEST_COMMAND_LOG\"\nif [ \"$1\" = is-active ]; then exit 0; fi\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RELAYPILOT_TEST_COMMAND_LOG", logPath)

	task := obj{
		"command":    "bind_endpoint",
		"agent_id":   "transit-la",
		"agent_role": "transit",
		"payload": obj{
			"inbound_tag": "vless-in",
			"auth_user":   "jp",
			"client_uuid": "55555555-5555-4555-8555-555555555555",
			"endpoint": obj{
				"kind":        endpointKind,
				"version":     version,
				"name":        "jp",
				"protocol":    "shadowsocks",
				"server":      "203.0.113.20",
				"server_port": 2443,
				"method":      "2022-blake3-aes-128-gcm",
				"password":    "MDEyMzQ1Njc4OWFiY2RlZg==",
				"tag":         "landing-jp-ss",
			},
		},
	}
	result := executeBindEndpointTask(task, state, conf)
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	apply := asObj(result["apply"])
	if apply["applied"] != true {
		t.Fatalf("apply = %#v", apply)
	}
	if asObj(apply["check"])["status"] != "ok" || asObj(apply["reload"])["status"] != "ok" || asObj(apply["restart"])["status"] != "skipped" || asObj(apply["status"])["status"] != "ok" {
		t.Fatalf("apply detail = %#v", apply)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "sing-box check -C "+conf) {
		t.Fatalf("missing sing-box check: %s", logText)
	}
	if !strings.Contains(logText, "systemctl reload sing-box") || strings.Contains(logText, "systemctl restart sing-box") || !strings.Contains(logText, "systemctl is-active sing-box") {
		t.Fatalf("missing reload/status or unexpected restart: %s", logText)
	}
	if !strings.Contains(str(result["text"]), "sing-box 已热重载并确认运行") {
		t.Fatalf("text = %#v", result["text"])
	}
}

func TestExecuteBindEndpointTaskFallsBackToRestartWhenReloadFails(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00-inbounds.json"), obj{"inbounds": []any{obj{"type": "vless", "tag": "vless-in", "users": []any{}}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "01-outbounds.json"), obj{"outbounds": []any{obj{"type": "direct", "tag": "direct"}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "02-route.json"), obj{"route": obj{"rules": []any{}, "final": "direct"}}, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "commands.log")
	binDir := filepath.Join(root, "bin")
	writeExecutable(t, filepath.Join(binDir, "sing-box"), "#!/usr/bin/env sh\nprintf 'sing-box %s\\n' \"$*\" >> \"$RELAYPILOT_TEST_COMMAND_LOG\"\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "systemctl"), "#!/usr/bin/env sh\nprintf 'systemctl %s\\n' \"$*\" >> \"$RELAYPILOT_TEST_COMMAND_LOG\"\nif [ \"$1\" = reload ]; then exit 9; fi\nif [ \"$1\" = is-active ]; then exit 0; fi\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RELAYPILOT_TEST_COMMAND_LOG", logPath)

	result := executeBindEndpointTask(obj{
		"command":    "bind_endpoint",
		"agent_id":   "transit-la",
		"agent_role": "transit",
		"payload": obj{
			"inbound_tag": "vless-in",
			"auth_user":   "jp",
			"client_uuid": "55555555-5555-4555-8555-555555555555",
			"endpoint": obj{
				"kind":        endpointKind,
				"version":     version,
				"name":        "jp",
				"protocol":    "shadowsocks",
				"server":      "203.0.113.20",
				"server_port": 2443,
				"method":      "2022-blake3-aes-128-gcm",
				"password":    "MDEyMzQ1Njc4OWFiY2RlZg==",
				"tag":         "landing-jp-ss",
			},
		},
	}, state, conf)
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	apply := asObj(result["apply"])
	if asObj(apply["reload"])["status"] != "failed" || asObj(apply["restart"])["status"] != "ok" || apply["applied"] != true {
		t.Fatalf("apply should fallback to restart: %#v", apply)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "systemctl reload sing-box") || !strings.Contains(logText, "systemctl restart sing-box") {
		t.Fatalf("missing reload fallback restart: %s", logText)
	}
	if !strings.Contains(str(result["text"]), "热重载失败后已重启并确认运行") {
		t.Fatalf("text = %#v", result["text"])
	}
}

func TestExecuteBindEndpointTaskFailsOnSingBoxCheckError(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00-inbounds.json"), obj{"inbounds": []any{obj{"type": "vless", "tag": "vless-in", "users": []any{}}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "01-outbounds.json"), obj{"outbounds": []any{obj{"type": "direct", "tag": "direct"}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "02-route.json"), obj{"route": obj{"rules": []any{}, "final": "direct"}}, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "commands.log")
	binDir := filepath.Join(root, "bin")
	writeExecutable(t, filepath.Join(binDir, "sing-box"), "#!/usr/bin/env sh\nprintf 'sing-box %s\\n' \"$*\" >> \"$RELAYPILOT_TEST_COMMAND_LOG\"\necho bad config >&2\nexit 7\n")
	writeExecutable(t, filepath.Join(binDir, "systemctl"), "#!/usr/bin/env sh\nprintf 'systemctl %s\\n' \"$*\" >> \"$RELAYPILOT_TEST_COMMAND_LOG\"\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RELAYPILOT_TEST_COMMAND_LOG", logPath)

	result := executeBindEndpointTask(obj{
		"command":    "bind_endpoint",
		"agent_id":   "transit-la",
		"agent_role": "transit",
		"payload": obj{
			"inbound_tag": "vless-in",
			"auth_user":   "jp",
			"client_uuid": "55555555-5555-4555-8555-555555555555",
			"endpoint": obj{
				"kind":        endpointKind,
				"version":     version,
				"name":        "jp",
				"protocol":    "shadowsocks",
				"server":      "203.0.113.20",
				"server_port": 2443,
				"method":      "2022-blake3-aes-128-gcm",
				"password":    "MDEyMzQ1Njc4OWFiY2RlZg==",
				"tag":         "landing-jp-ss",
			},
		},
	}, state, conf)
	if result["success"] != false || !strings.Contains(str(result["error"]), "sing-box check failed") {
		t.Fatalf("result = %#v", result)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if strings.Contains(logText, "systemctl restart") {
		t.Fatalf("restart should not run after failed check: %s", logText)
	}
}

func TestHubLinkTransitLandingMeshModeWritesOverlayEndpoint(t *testing.T) {
	t.Setenv("RELAYPILOT_DISABLE_DATAPLANE_APPLY", "1")
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	landingState := filepath.Join(root, "landing")
	transitState := filepath.Join(root, "transit")
	transitConf := filepath.Join(root, "transit-conf")
	wgDir := filepath.Join(root, "wg")
	if _, err := ensureTransitReality(transitConf, transitState, "::", 443, "vless-in", "www.cloudflare.com", "www.cloudflare.com", 443, "", "0123456789abcdef", "1m", false); err != nil {
		t.Fatal(err)
	}
	endpoint := obj{
		"kind":        endpointKind,
		"version":     version,
		"name":        "jp",
		"protocol":    "shadowsocks",
		"server":      "203.0.113.20",
		"server_port": 2443,
		"method":      "2022-blake3-aes-128-gcm",
		"password":    "MDEyMzQ1Njc4OWFiY2RlZg==",
		"tag":         "landing-jp-ss",
		"network":     "tcp,udp",
	}
	if err := writeJSON(filepath.Join(landingState, "endpoints", "jp.json"), endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
		},
		"landing-jp": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-jp",
			"role":      "landing",
			"name":      "JP Landing",
			"transport": "poll",
			"topology": obj{"endpoints": []any{
				obj{"name": "jp", "protocol": "shadowsocks", "server": "203.0.113.20", "server_port": 2443, "tag": "landing-jp-ss"},
			}},
		},
	}
	if err := saveHubRegistry(hubState, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := issueHubAgentToken(hubState, "transit-la", "test-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := issueHubAgentToken(hubState, "landing-jp", "landing-token"); err != nil {
		t.Fatal(err)
	}
	queued, err := hubDispatchCommand(hubState, "/link transit-la landing-jp user-jp --mode mesh --mesh-cidr 10.99.0.0/30 --mesh-port 50123 --mesh-no-up --mesh-config-dir "+wgDir+" --inbound-tag vless-in")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queued, "模式：自动组网") || !strings.Contains(queued, "transit 10.99.0.1 ↔ landing 10.99.0.2") {
		t.Fatalf("queued = %s", queued)
	}
	srv := httptest.NewServer(&hubServer{stateDir: hubState, quiet: true})
	defer srv.Close()
	if _, err := agentPollOnce(srv.URL, "landing-jp", "landing-token", "landing", landingState, filepath.Join(root, "landing-conf"), 1, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wgDir, "rpeee049da.conf")); err != nil {
		t.Fatalf("landing wireguard config not written: %v", err)
	}
	tasks, err := listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	var bindQueued obj
	for _, task := range tasks {
		if str(task["command"]) == "export_endpoint" {
			payload := asObj(task["payload"])
			if str(asObj(payload["mesh_landing"])["private_key"]) != "" || str(asObj(payload["mesh_landing"])["pre_shared_key"]) != "" {
				t.Fatalf("completed export task retained mesh secrets: %#v", task)
			}
		}
		if str(task["command"]) == "bind_endpoint" {
			bindQueued = task
		}
	}
	if len(bindQueued) == 0 {
		t.Fatalf("bind task not queued: %#v", tasks)
	}
	queuedEndpoint := asObj(asObj(bindQueued["payload"])["endpoint"])
	if queuedEndpoint["server"] != "10.99.0.2" || queuedEndpoint["direct_server"] != "203.0.113.20" || queuedEndpoint["link_mode"] != "mesh" {
		t.Fatalf("queued mesh endpoint = %#v", queuedEndpoint)
	}
	meshTransit := asObj(asObj(bindQueued["payload"])["mesh_transit"])
	if str(meshTransit["peer_endpoint"]) != "203.0.113.20:50123" {
		t.Fatalf("mesh transit peer endpoint = %#v", meshTransit)
	}
	if _, err := agentPollOnce(srv.URL, "transit-la", "test-token", "transit", transitState, transitConf, 1, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	imported, err := loadEndpoint(filepath.Join(transitState, "endpoints", "jp.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	if imported["server"] != "10.99.0.2" || imported["link_mode"] != "mesh" {
		t.Fatalf("imported endpoint = %#v", imported)
	}
	tasks, err = listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if str(task["command"]) != "bind_endpoint" {
			continue
		}
		payload := asObj(task["payload"])
		if str(asObj(payload["endpoint"])["password"]) != "" {
			t.Fatalf("completed bind task retained endpoint password: %#v", task)
		}
		if str(asObj(payload["mesh_transit"])["private_key"]) != "" || str(asObj(payload["mesh_transit"])["pre_shared_key"]) != "" {
			t.Fatalf("completed bind task retained mesh secrets: %#v", task)
		}
	}
	summary, err := inspectConfig(transitConf)
	if err != nil {
		t.Fatal(err)
	}
	authRoutes := asList(asObj(summary["route"])["auth_routes"])
	if len(authRoutes) != 1 || str(asObj(authRoutes[0])["outbound"]) != "landing-jp-ss" {
		t.Fatalf("auth routes = %#v", authRoutes)
	}
}

func TestPublicEntryOverridesExportedLandingEndpointAndMeshPeer(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	landingState := filepath.Join(root, "landing")
	wgDir := filepath.Join(root, "wg")
	endpoint := obj{
		"kind":        endpointKind,
		"version":     version,
		"name":        "jp",
		"protocol":    "shadowsocks",
		"server":      "10.0.0.8",
		"server_port": 2443,
		"method":      "2022-blake3-aes-128-gcm",
		"password":    "MDEyMzQ1Njc4OWFiY2RlZg==",
		"tag":         "landing-jp-ss",
		"network":     "tcp,udp",
	}
	if err := writeJSON(filepath.Join(landingState, "endpoints", "jp.json"), endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setPublicEntry(landingState, publicEntryOptions{Use: "shadowsocks", Name: "jp", Host: "front.example", PublicPort: 443, LocalPort: 2443, Network: "tcp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := setPublicEntry(landingState, publicEntryOptions{Use: "wireguard", Name: "jp", Host: "front.example", PublicPort: 51820, LocalPort: 50123, Network: "udp"}); err != nil {
		t.Fatal(err)
	}
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{"kind": agentRegistrationKind, "version": version, "id": "transit-la", "role": "transit", "name": "LA Transit", "transport": "poll"},
		"landing-jp": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-jp",
			"role":      "landing",
			"name":      "JP Landing",
			"transport": "poll",
			"topology": obj{"endpoints": []any{
				obj{"name": "jp", "protocol": "shadowsocks", "server": "10.0.0.8", "server_port": 2443, "tag": "landing-jp-ss"},
			}},
		},
	}
	if err := saveHubRegistry(hubState, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := issueHubAgentToken(hubState, "landing-jp", "landing-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := hubDispatchCommand(hubState, "/link transit-la landing-jp user-jp --mode mesh --mesh-cidr 10.99.0.0/30 --mesh-port 50123 --mesh-no-up --mesh-config-dir "+wgDir); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&hubServer{stateDir: hubState, quiet: true})
	defer srv.Close()
	if _, err := agentPollOnce(srv.URL, "landing-jp", "landing-token", "landing", landingState, filepath.Join(root, "landing-conf"), 1, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	tasks, err := listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	var bindQueued obj
	for _, task := range tasks {
		if str(task["command"]) == "bind_endpoint" {
			bindQueued = task
		}
	}
	if len(bindQueued) == 0 {
		t.Fatalf("bind task not queued: %#v", tasks)
	}
	queuedEndpoint := asObj(asObj(bindQueued["payload"])["endpoint"])
	if queuedEndpoint["direct_server"] != "front.example" || int64Value(queuedEndpoint["direct_server_port"]) != 443 {
		t.Fatalf("public entry did not override exported endpoint: %#v", queuedEndpoint)
	}
	meshTransit := asObj(asObj(bindQueued["payload"])["mesh_transit"])
	if str(meshTransit["peer_endpoint"]) != "front.example:51820" {
		t.Fatalf("public entry did not override mesh peer endpoint: %#v", meshTransit)
	}
}

func TestSetPublicEntryNormalizesHostPortAndDefaultsNetwork(t *testing.T) {
	stateDir := t.TempDir()
	entry, err := setPublicEntry(stateDir, publicEntryOptions{Use: "wg", Name: "jp", Host: "https://front.example:51820"})
	if err != nil {
		t.Fatal(err)
	}
	if str(entry["use"]) != "wireguard" || str(entry["host"]) != "front.example" || int64Value(entry["public_port"]) != 51820 || str(entry["network"]) != "udp" {
		t.Fatalf("normalized entry = %#v", entry)
	}
	if _, err := setPublicEntry(stateDir, publicEntryOptions{Use: "shadowsocks", Name: "jp", Host: "front.example/path", PublicPort: 443}); err == nil {
		t.Fatal("expected path-bearing host to be rejected")
	}
}

func TestEnsureWireGuardMeshRefusesUnmanagedConfig(t *testing.T) {
	root := t.TempDir()
	privateKey, publicKey, err := generateWireGuardKeypair()
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "wg")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "rpmanual.conf"), []byte("[Interface]\nPrivateKey = manual\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ensureWireGuardMesh(obj{
		"role":             "landing",
		"interface":        "rpmanual",
		"config_dir":       configDir,
		"address":          "10.99.0.2/32",
		"listen_port":      50123,
		"private_key":      privateKey,
		"peer_public_key":  publicKey,
		"peer_allowed_ips": "10.99.0.1/32",
		"auto_up":          false,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite non-RelayPilot") {
		t.Fatalf("expected unmanaged config refusal, got %v", err)
	}
}

func TestRemoveLandingQueuesTransitUnbindCleanup(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	transitState := filepath.Join(root, "transit")
	conf := filepath.Join(root, "conf")
	endpoint := obj{
		"kind":        endpointKind,
		"version":     version,
		"name":        "jp",
		"protocol":    "shadowsocks",
		"server":      "203.0.113.20",
		"server_port": 2443,
		"method":      "2022-blake3-aes-128-gcm",
		"password":    "MDEyMzQ1Njc4OWFiY2RlZg==",
		"tag":         "landing-jp-ss",
	}
	if err := writeJSON(filepath.Join(transitState, "endpoints", "jp.json"), endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds": []any{obj{"type": "vless", "tag": "vless-in", "users": []any{
			obj{"name": "user-jp", "uuid": "11111111-1111-4111-8111-111111111111"},
			obj{"name": "other-jp", "uuid": "33333333-3333-4333-8333-333333333333"},
			obj{"name": "keep", "uuid": "22222222-2222-4222-8222-222222222222"},
		}}},
		"outbounds": []any{
			obj{"type": "shadowsocks", "tag": "landing-jp-ss", "server": "203.0.113.20", "server_port": 2443, "method": "2022-blake3-aes-128-gcm", "password": "MDEyMzQ1Njc4OWFiY2RlZg=="},
			obj{"type": "direct", "tag": "direct"},
		},
		"route": obj{"rules": []any{
			obj{"inbound": []any{"vless-in"}, "auth_user": []any{"user-jp"}, "outbound": "landing-jp-ss", "action": "route"},
			obj{"inbound": []any{"vless-in"}, "auth_user": []any{"other-jp"}, "outbound": "landing-jp-ss", "action": "route"},
			obj{"inbound": []any{"vless-in"}, "auth_user": []any{"keep"}, "outbound": "direct", "action": "route"},
		}, "final": "direct"},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":    agentRegistrationKind,
			"version": version,
			"id":      "transit-la",
			"role":    "transit",
			"name":    "LA Transit",
			"topology": obj{"links": []any{
				obj{"auth_user": "user-jp", "outbound_tag": "landing-jp-ss", "endpoint_name": "jp", "server": "203.0.113.20", "server_port": 2443},
			}},
		},
		"landing-jp": obj{
			"kind":    agentRegistrationKind,
			"version": version,
			"id":      "landing-jp",
			"role":    "landing",
			"name":    "JP Landing",
			"topology": obj{"endpoints": []any{
				obj{"name": "jp", "tag": "landing-jp-ss", "server": "203.0.113.20", "server_port": 2443},
			}},
		},
	}
	if err := saveHubRegistry(hubState, reg); err != nil {
		t.Fatal(err)
	}
	removed, err := removeHubAgent(hubState, "landing-jp", "test-remove", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if str(removed["removed"]) != "landing-jp" {
		t.Fatalf("removed = %#v", removed)
	}
	tasks, err := listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || str(tasks[0]["command"]) != "unbind_endpoint" || str(tasks[0]["agent_id"]) != "transit-la" {
		t.Fatalf("cleanup tasks = %#v", tasks)
	}
	result := executeTask(tasks[0], transitState, conf)
	if result["success"] != true {
		t.Fatalf("unbind result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(transitState, "endpoints", "jp.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("endpoint should be removed, stat err = %v", err)
	}
	summary, err := inspectConfig(conf)
	if err != nil {
		t.Fatal(err)
	}
	authRoutes := asList(asObj(summary["route"])["auth_routes"])
	if len(authRoutes) != 1 || !listContains(asObj(authRoutes[0])["auth_user"], "keep") {
		t.Fatalf("auth routes after cleanup = %#v", authRoutes)
	}
	for _, inbound := range asList(summary["inbounds"]) {
		users := asList(asObj(inbound)["users"])
		for _, raw := range users {
			if str(asObj(raw)["name"]) == "user-jp" || str(asObj(raw)["name"]) == "other-jp" {
				t.Fatalf("removed landing user still present: %#v", users)
			}
		}
	}
	for _, outbound := range asList(summary["outbounds"]) {
		if str(asObj(outbound)["tag"]) == "landing-jp-ss" {
			t.Fatalf("orphan outbound still present: %#v", summary["outbounds"])
		}
	}
}

func TestRemoveTransitQueuesLandingMeshTeardown(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	wgDir := filepath.Join(root, "wg")
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":    agentRegistrationKind,
			"version": version,
			"id":      "transit-la",
			"role":    "transit",
			"name":    "LA Transit",
			"topology": obj{"links": []any{
				obj{"auth_user": "user-jp", "outbound_tag": "landing-jp-ss", "endpoint_name": "jp", "server": "10.99.0.2", "server_port": 2443, "link_mode": "mesh", "mesh": obj{"interface": "rpcleanup", "config_dir": wgDir}},
			}},
		},
		"landing-jp": obj{
			"kind":    agentRegistrationKind,
			"version": version,
			"id":      "landing-jp",
			"role":    "landing",
			"name":    "JP Landing",
			"topology": obj{"endpoints": []any{
				obj{"name": "jp", "tag": "landing-jp-ss", "server": "10.99.0.2", "server_port": 2443},
			}},
		},
	}
	if err := saveHubRegistry(hubState, reg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wgDir, "rpcleanup.conf"), []byte(wireGuardConfigMarker+"\n[Interface]\nPrivateKey = test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := removeHubAgent(hubState, "transit-la", "test-remove", true, true); err != nil {
		t.Fatal(err)
	}
	tasks, err := listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || str(tasks[0]["command"]) != "teardown_mesh" || str(tasks[0]["agent_id"]) != "landing-jp" {
		t.Fatalf("cleanup tasks = %#v", tasks)
	}
	result := executeTask(tasks[0], filepath.Join(root, "landing-state"), filepath.Join(root, "landing-conf"))
	if result["success"] != true {
		t.Fatalf("teardown result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(wgDir, "rpcleanup.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wireguard config should be removed, stat err = %v", err)
	}
}

func TestHubNativeTLSMTLSPollFlow(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	agentState := filepath.Join(root, "agent")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds":  []any{obj{"type": "vless", "tag": "vless-in", "users": []any{obj{"name": "hk", "uuid": "u"}}}},
		"outbounds": []any{obj{"type": "direct", "tag": "direct"}},
		"route":     obj{"rules": []any{}, "final": "direct"},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	regFile := filepath.Join(root, "transit.registration.json")
	if err := writeJSON(regFile, obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        "transit-hk",
		"role":      "transit",
		"name":      "HK Transit",
		"transport": "poll",
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importAgentRegistration(hubState, regFile); err != nil {
		t.Fatal(err)
	}
	if _, err := issueHubAgentToken(hubState, "transit-hk", "test-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := hubDispatchCommand(hubState, "/status transit-hk"); err != nil {
		t.Fatal(err)
	}

	tlsBundle, err := initHubTLS(hubState, hubTLSInitOptions{Hosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	agentCert, err := issueHubAgentClientCert(hubState, "transit-hk", filepath.Join(root, "agent-cert"), 365)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := loadHubServerTLSConfig(tlsBundle.ServerCertPath, tlsBundle.ServerKeyPath, tlsBundle.CACertPath, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(&hubServer{stateDir: hubState, quiet: true, requireClientCert: true})
	srv.TLS = serverTLS
	srv.StartTLS()
	defer srv.Close()

	agentTLS, err := loadAgentTLSConfig(tlsBundle.CACertPath, agentCert.CertPath, agentCert.KeyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := agentPollOnceWithTLS(srv.URL, "transit-hk", "test-token", "transit", agentState, conf, 1, 5*time.Second, agentTLS)
	if err != nil {
		t.Fatal(err)
	}
	if res["leased"] != 1 {
		t.Fatalf("leased = %#v", res["leased"])
	}
	tasks, err := listHubTasks(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0]["status"] != "done" {
		t.Fatalf("tasks = %#v", tasks)
	}

	noClientCertTLS, err := loadAgentTLSConfig(tlsBundle.CACertPath, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentPollOnceWithTLS(srv.URL, "transit-hk", "test-token", "transit", agentState, conf, 1, 5*time.Second, noClientCertTLS); err == nil {
		t.Fatal("mTLS hub accepted a client without an agent certificate")
	}
	wrongRegFile := filepath.Join(root, "wrong.registration.json")
	if err := writeJSON(wrongRegFile, obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        "other-agent",
		"role":      "transit",
		"transport": "poll",
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importAgentRegistration(hubState, wrongRegFile); err != nil {
		t.Fatal(err)
	}
	wrongCert, err := issueHubAgentClientCert(hubState, "other-agent", filepath.Join(root, "wrong-cert"), 365)
	if err != nil {
		t.Fatal(err)
	}
	wrongTLS, err := loadAgentTLSConfig(tlsBundle.CACertPath, wrongCert.CertPath, wrongCert.KeyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentPollOnceWithTLS(srv.URL, "transit-hk", "test-token", "transit", agentState, conf, 1, 5*time.Second, wrongTLS); err == nil {
		t.Fatal("mTLS hub accepted a certificate for a different agent id")
	}
}

func TestHubProvisionBundleAgentEnrollsAndPolls(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	agentState := filepath.Join(root, "agent")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds":  []any{obj{"type": "shadowsocks", "tag": "ss-in", "users": []any{}}},
		"outbounds": []any{obj{"type": "direct", "tag": "direct"}},
		"route":     obj{"rules": []any{}, "final": "direct"},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	tlsBundle, err := initHubTLS(hubState, hubTLSInitOptions{Hosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	provisioned, err := provisionHubAgentBundle(hubState, hubAgentProvisionOptions{
		HubURL:  "https://127.0.0.1:8443",
		AgentID: "landing-jp",
		Role:    "landing",
		Name:    "Landing JP",
		Labels:  "region=jp",
		Days:    365,
	})
	if err != nil {
		t.Fatal(err)
	}
	if str(provisioned["bundle"]) == "" || strings.Contains(str(provisioned["install_command"]), str(asObj(provisioned["secret_preview"])["token"])) {
		t.Fatalf("bad provision response: %#v", provisioned)
	}
	agents, err := listHubAgents(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || str(agents[0]["id"]) != "landing-jp" || str(asObj(agents[0]["labels"])["region"]) != "jp" {
		t.Fatalf("agents = %#v", agents)
	}

	enrolled, err := agentEnrollBundle(str(provisioned["bundle"]), agentEnrollOptions{StateDir: agentState})
	if err != nil {
		t.Fatal(err)
	}
	if str(enrolled["agent_id"]) != "landing-jp" || str(enrolled["token_file"]) == "" || str(enrolled["client_key"]) == "" {
		t.Fatalf("enrolled = %#v", enrolled)
	}
	for _, path := range []string{str(enrolled["ca_cert"]), str(enrolled["client_cert"]), str(enrolled["client_key"]), str(enrolled["token_file"])} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing enrolled file %s: %v", path, err)
		}
	}

	serverTLS, err := loadHubServerTLSConfig(tlsBundle.ServerCertPath, tlsBundle.ServerKeyPath, tlsBundle.CACertPath, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(&hubServer{stateDir: hubState, quiet: true})
	srv.TLS = serverTLS
	srv.StartTLS()
	defer srv.Close()

	pollCfg, err := loadAgentEnrollment(agentState)
	if err != nil {
		t.Fatal(err)
	}
	pollCfg.HubURL = srv.URL
	tlsConfig, err := loadAgentTLSConfig(pollCfg.CACertPath, pollCfg.ClientCertPath, pollCfg.ClientKeyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	tokenBytes, err := os.ReadFile(pollCfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agentPollOnceWithTLS(pollCfg.HubURL, pollCfg.AgentID, strings.TrimSpace(string(tokenBytes)), pollCfg.Role, agentState, conf, 1, 5*time.Second, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	if res["leased"] != 0 {
		t.Fatalf("leased = %#v", res["leased"])
	}
}

func TestResolveHubPublicURLUsesExplicitURLHostOrDetector(t *testing.T) {
	got, err := resolveHubPublicURL(hubPublicURLOptions{ExplicitURL: "https://hub.example:9443", Port: 8443}, func() (string, error) {
		return "203.0.113.10", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://hub.example:9443" {
		t.Fatalf("explicit url = %q", got)
	}

	got, err = resolveHubPublicURL(hubPublicURLOptions{PublicHost: "hub.example", Port: 8443}, func() (string, error) {
		return "203.0.113.10", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://hub.example:8443" {
		t.Fatalf("public host url = %q", got)
	}

	got, err = resolveHubPublicURL(hubPublicURLOptions{Port: 8443}, func() (string, error) {
		return "203.0.113.10", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://203.0.113.10:8443" {
		t.Fatalf("detected ip url = %q", got)
	}

	got, err = resolveHubPublicURL(hubPublicURLOptions{Port: 443}, func() (string, error) {
		return "2001:db8::1", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://[2001:db8::1]" {
		t.Fatalf("detected ipv6 url = %q", got)
	}
}

func TestResolveHubPublicURLRequiresReachableIdentity(t *testing.T) {
	if _, err := resolveHubPublicURL(hubPublicURLOptions{Port: 8443}, func() (string, error) {
		return "", errors.New("offline")
	}); err == nil {
		t.Fatal("expected missing public URL detection to fail")
	}
}

func TestHubCreateEnrollCodeCLIAcceptsPublicHost(t *testing.T) {
	root := t.TempDir()
	if _, err := initHubTLS(root, hubTLSInitOptions{Hosts: []string{"hub.example"}}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = run([]string{
		"hub-create-enroll-code",
		"--state-dir", root,
		"--public-host", "hub.example",
		"--port", "9443",
		"--agent-id", "agent-cli",
		"--role", "transit",
	})
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&stdout, r); err != nil {
		t.Fatal(err)
	}
	var out obj
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("bad json %q: %v", stdout.String(), err)
	}
	if str(out["hub_url"]) != "https://hub.example:9443" {
		t.Fatalf("hub_url = %#v", out["hub_url"])
	}
}

func TestHubCreateEnrollCodeReusesExistingPendingAgentDefaults(t *testing.T) {
	root := t.TempDir()
	if _, err := initHubTLS(root, hubTLSInitOptions{Hosts: []string{"hub.example"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := createHubEnrollInvite(root, hubEnrollCodeOptions{
		HubURL:  "https://hub.example:9443",
		AgentID: "landing-jp",
		Role:    "landing",
		Name:    "Landing JP",
		Labels:  "region=jp",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := createHubEnrollInvite(root, hubEnrollCodeOptions{
		HubURL:  "https://hub.example:9443",
		AgentID: "landing-jp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if str(created["role"]) != "landing" {
		t.Fatalf("role = %#v", created["role"])
	}
	agents, err := listHubAgents(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || str(agents[0]["role"]) != "landing" || str(agents[0]["name"]) != "Landing JP" || str(asObj(agents[0]["labels"])["region"]) != "jp" {
		t.Fatalf("agents = %#v", agents)
	}
}

func TestResolveHubPublicURLAcceptsURLAsPublicHost(t *testing.T) {
	tests := []struct {
		name       string
		publicHost string
		port       int
		want       string
	}{
		{
			name:       "https URL without port uses selected port",
			publicHost: "https://hub.example",
			port:       9443,
			want:       "https://hub.example:9443",
		},
		{
			name:       "https URL with port keeps URL port",
			publicHost: "https://hub.example:27779",
			port:       9443,
			want:       "https://hub.example:27779",
		},
		{
			name:       "host with port keeps host port",
			publicHost: "hub.example:27779",
			port:       9443,
			want:       "https://hub.example:27779",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHubPublicURL(hubPublicURLOptions{PublicHost: tt.publicHost, Port: tt.port}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("hub url = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeTLSHostsAcceptsURLAndPort(t *testing.T) {
	got := normalizeTLSHosts([]string{
		"https://hub.example:27779",
		"198.51.100.10:27779",
		"[2001:db8::1]:27779",
	})
	for _, want := range []string{"hub.example", "198.51.100.10", "2001:db8::1"} {
		if !slices.Contains(got, want) {
			t.Fatalf("normalizeTLSHosts() = %#v, missing %q", got, want)
		}
	}
	for _, invalid := range []string{"https://hub.example:27779", "198.51.100.10:27779", "[2001:db8::1]:27779"} {
		if slices.Contains(got, invalid) {
			t.Fatalf("normalizeTLSHosts() = %#v, should not include raw input %q", got, invalid)
		}
	}
}

func TestHubInviteAgentInitiatedEnrollmentAndPolls(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	agentState := filepath.Join(root, "agent")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds":  []any{obj{"type": "shadowsocks", "tag": "ss-in", "users": []any{}}},
		"outbounds": []any{obj{"type": "direct", "tag": "direct"}},
		"route":     obj{"rules": []any{}, "final": "direct"},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	tlsBundle, err := initHubTLS(hubState, hubTLSInitOptions{Hosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := loadHubServerTLSConfig(tlsBundle.ServerCertPath, tlsBundle.ServerKeyPath, tlsBundle.CACertPath, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(&hubServer{stateDir: hubState, quiet: true, requireClientCert: true})
	srv.TLS = serverTLS
	srv.StartTLS()
	defer srv.Close()

	created, err := createHubEnrollInvite(hubState, hubEnrollCodeOptions{
		HubURL:     srv.URL,
		AgentID:    "landing-jp",
		Role:       "landing",
		Name:       "Landing JP",
		Labels:     "region=jp",
		TTLSeconds: 600,
		Days:       365,
	})
	if err != nil {
		t.Fatal(err)
	}
	invite := str(created["invite"])
	if invite == "" || strings.Contains(str(created["install_command"]), str(created["code"])) {
		t.Fatalf("bad invite response: %#v", created)
	}
	pendingAgents, err := listHubAgents(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingAgents) != 1 {
		t.Fatalf("pending agents = %#v", pendingAgents)
	}
	pending := pendingAgents[0]
	if str(pending["id"]) != "landing-jp" || str(pending["enrollment_status"]) != "pending" || int64Value(pending["last_seen"]) != 0 {
		t.Fatalf("pending agent = %#v", pending)
	}
	if agentLiveness(pending) != "pending" {
		t.Fatalf("pending liveness = %s", agentLiveness(pending))
	}
	if listing := formatAgentsText(pendingAgents); !strings.Contains(listing, "待接入") || !strings.Contains(listing, "🕓") {
		t.Fatalf("pending listing = %q", listing)
	}
	if text := formatHubEnrollInviteText(created); !strings.Contains(text, "Agent 邀请码已生成") || !strings.Contains(text, "10 分钟") || !strings.Contains(text, "--enroll") || !strings.Contains(text, "单次使用") {
		t.Fatalf("invite text = %q", text)
	}

	enrolled, err := agentEnrollInvite(invite, agentEnrollOptions{StateDir: agentState, IPMode: "dynamic", PublicIPIntervalSeconds: 600}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if str(enrolled["agent_id"]) != "landing-jp" || str(enrolled["role"]) != "landing" {
		t.Fatalf("enrolled = %#v", enrolled)
	}
	if _, err := agentEnrollInvite(invite, agentEnrollOptions{StateDir: filepath.Join(root, "agent-reuse")}, 10*time.Second); err == nil {
		t.Fatal("single-use invite was accepted twice")
	}
	agents, err := listHubAgents(hubState)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || str(agents[0]["id"]) != "landing-jp" || str(asObj(agents[0]["labels"])["region"]) != "jp" {
		t.Fatalf("agents = %#v", agents)
	}
	if str(agents[0]["enrollment_status"]) != "" || agentLiveness(agents[0]) != "online" {
		t.Fatalf("registered agent = %#v", agents[0])
	}

	cfg, err := loadAgentEnrollment(agentState)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IPMode != "dynamic" || cfg.PublicIPIntervalSeconds != 600 {
		t.Fatalf("agent enrollment ip mode = %#v", cfg)
	}
	tlsConfig, err := loadAgentTLSConfig(cfg.CACertPath, cfg.ClientCertPath, cfg.ClientKeyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	tokenBytes, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agentPollOnceWithTLS(cfg.HubURL, cfg.AgentID, strings.TrimSpace(string(tokenBytes)), cfg.Role, agentState, conf, 1, 5*time.Second, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	if res["leased"] != 0 {
		t.Fatalf("leased = %#v", res["leased"])
	}
}

func TestSignedPostCanonicalBody(t *testing.T) {
	root := t.TempDir()
	regFile := filepath.Join(root, "a.json")
	_ = writeJSON(regFile, obj{"kind": agentRegistrationKind, "version": version, "id": "agent-a", "role": "landing"}, 0o600)
	_, _ = importAgentRegistration(root, regFile)
	_, _ = issueHubAgentToken(root, "agent-a", "secret")
	body := obj{"topology": obj{}, "health": obj{"status": "ok"}}
	wire, _ := json.Marshal(body)
	ts := strconvNow()
	nonce := "n"
	sig, err := signAgentRequest("secret", "POST", "/api/agents/agent-a/heartbeat", wire, ts, nonce, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if !verifySignature(root, "agent-a", "POST", "/api/agents/agent-a/heartbeat", wire, ts, nonce, sig) {
		t.Fatal("valid signature rejected")
	}
	nonce2 := "n2"
	sig2, err := signAgentRequest("secret", "POST", "/api/agents/agent-a/heartbeat", wire, ts, nonce2, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if verifySignature(root, "agent-a", "POST", "/api/agents/agent-a/heartbeat", append(bytes.Clone(wire), ' '), ts, nonce2, sig2) {
		t.Fatal("altered body signature accepted")
	}
}

func TestMigrateStateDirPreservesFilesAndConflicts(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "source")
	to := filepath.Join(root, "relay")
	stringList := func(v any) []string {
		if xs, ok := v.([]string); ok {
			return xs
		}
		out := []string{}
		for _, raw := range asList(v) {
			out = append(out, str(raw))
		}
		return out
	}
	if err := writeJSON(filepath.Join(from, "endpoints", "hk.json"), obj{"name": "hk", "secret": "s"}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "agent-token"), []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(from, "agent-token"), filepath.Join(from, "linked-token")); err != nil {
		t.Fatal(err)
	}
	dry, err := migrateStateDir(from, to, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry["ok"] != true || len(stringList(dry["copied"])) != 2 {
		t.Fatalf("dry result = %#v", dry)
	}
	if len(stringList(dry["skipped"])) != 1 || stringList(dry["skipped"])[0] != "linked-token" {
		t.Fatalf("dry skipped = %#v", dry)
	}
	if _, err := os.Stat(filepath.Join(to, "agent-token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run created file: %v", err)
	}
	res, err := migrateStateDir(from, to, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res["ok"] != true {
		t.Fatalf("migrate result = %#v", res)
	}
	info, err := os.Stat(filepath.Join(to, "endpoints", "hk.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	conflict, err := migrateStateDir(from, to, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if conflict["ok"] != false || len(stringList(conflict["conflicts"])) != 2 {
		t.Fatalf("conflict result = %#v", conflict)
	}
	forced, err := migrateStateDir(from, to, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if forced["ok"] != true || len(stringList(forced["conflicts"])) != 0 {
		t.Fatalf("forced result = %#v", forced)
	}
	if _, err := migrateStateDir(from, filepath.Join(from, "nested"), false, true); err == nil {
		t.Fatal("nested migration target accepted")
	}
}

func TestHubDispatchTopologySelectorsAndHubDoctor(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	nowTs := now()
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-hk": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-hk",
			"role":      "transit",
			"name":      "HK Transit",
			"labels":    obj{"region": "hk"},
			"last_seen": nowTs,
			"topology": obj{"links": []any{
				obj{"auth_user": "alice", "outbound_tag": "landing-hk-ss", "endpoint_name": "hk"},
			}, "endpoints": []any{}, "inbounds": []any{}},
		},
		"landing-hk": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-hk",
			"role":      "landing",
			"name":      "HK Landing",
			"labels":    obj{"region": "hk"},
			"last_seen": nowTs - heartbeatOfflineSeconds - 1,
			"topology": obj{"links": []any{}, "endpoints": []any{
				obj{"name": "hk", "tag": "landing-hk-ss", "server": "203.0.113.10", "server_port": 443},
			}, "inbounds": []any{}},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}

	topology, err := hubDispatchCommand(state, "/topology")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(topology, "转发拓扑") || !strings.Contains(topology, "landing-hk") || !strings.Contains(topology, "user:alice") {
		t.Fatalf("topology text = %s", topology)
	}
	status, err := hubDispatchCommand(state, "/status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "异常：1 离线") || !strings.Contains(status, "巡检：面板 → 刷新节点详情") {
		t.Fatalf("status text = %s", status)
	}
	agents, err := hubDispatchCommand(state, "/agents")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agents, "region=hk") || !strings.Contains(agents, "🔴") {
		t.Fatalf("agents text = %s", agents)
	}
	doctor, err := hubDispatchCommand(state, "/doctor hub")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doctor, "hub doctor:") || !strings.Contains(doctor, "agents=2") {
		t.Fatalf("doctor text = %s", doctor)
	}
	queued, err := hubDispatchCommand(state, "/endpoints label:region=hk")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queued, "给 2 个节点") {
		t.Fatalf("label selector queue = %s", queued)
	}
	if err := writeJSON(filepath.Join(state, hubAlertsName), obj{
		"kind":    "relaypilot/hub-alerts",
		"version": version,
		"alerts": obj{"landing-hk": obj{
			"agent_id":            "landing-hk",
			"status":              "alerted",
			"agent":               obj{"name": "HK Landing"},
			"offline_age_seconds": 7200,
			"updated_at":          nowTs,
		}},
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	alerts, err := hubDispatchCommand(state, "/alerts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(alerts, "离线告警：1") || !strings.Contains(alerts, "landing-hk") {
		t.Fatalf("alerts text = %s", alerts)
	}
}

func TestTelegramHubUpdateDispatchAndAPIEncoding(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	nowTs := now()
	reg := defaultHubRegistry()
	reg["agents"] = obj{"transit-hk": obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        "transit-hk",
		"role":      "transit",
		"name":      "HK Transit",
		"transport": "poll",
		"labels":    obj{"region": "hk"},
		"last_seen": nowTs,
		"sync_at":   nowTs - 180,
		"network": obj{
			"ip_mode":   "dynamic",
			"public_ip": "8.8.8.8",
			"location":  obj{"label": "中国香港", "ip": "8.8.8.8"},
		},
		"topology": obj{"links": []any{
			obj{"auth_user": "user-hk", "outbound_tag": "landing-hk-ss", "endpoint_name": "hk", "server": "203.0.113.20", "server_port": 443},
		}},
	}, "landing-hk": obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        "landing-hk",
		"role":      "landing",
		"name":      "HK Landing",
		"transport": "poll",
		"labels":    obj{"region": "hk"},
		"last_seen": nowTs,
		"sync_at":   nowTs - 240,
		"topology": obj{"endpoints": []any{
			obj{"name": "hk", "tag": "landing-hk-ss", "server": "203.0.113.20", "server_port": 443},
		}},
	}, "172.81.102.137": obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        "172.81.102.137",
		"role":      "transit",
		"name":      "IP Named Transit",
		"transport": "poll",
		"last_seen": nowTs,
		"sync_at":   nowTs - 60,
		"network": obj{
			"ip_mode":     "static",
			"observed_ip": "172.81.102.137",
		},
	}}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	reply := handleTelegramHubUpdate(state, obj{"update_id": 1, "message": obj{"text": "/status"}})
	if reply != "" {
		t.Fatalf("unprefixed /status should be ignored to avoid cross-service conflicts: %s", reply)
	}
	prefixedReply := handleTelegramHubUpdate(state, obj{"update_id": 2, "message": obj{"text": "/relaypilot_status"}})
	if !strings.Contains(prefixedReply, "Hub 管理面正常") {
		t.Fatalf("prefixed reply = %s", prefixedReply)
	}
	startReply := handleTelegramHubReply(state, obj{"update_id": 4, "message": obj{"text": "/start", "chat": obj{"id": "999"}}})
	if startReply.Text != "" || len(startReply.ReplyMarkup) != 0 {
		t.Fatalf("/start should be ignored to avoid cross-service conflicts: %#v", startReply)
	}
	panelReply := handleTelegramHubReply(state, obj{"update_id": 4, "message": obj{"text": "/relaypilot", "chat": obj{"id": "999"}}})
	if !strings.Contains(panelReply.Text, "RelayPilot 控制中枢") || panelReply.ParseMode != "HTML" || len(panelReply.ReplyMarkup) == 0 {
		t.Fatalf("panel reply = %#v", panelReply)
	}
	legacyPanelReply := handleTelegramHubReply(state, obj{"update_id": 15, "message": obj{"text": "/relaypilot_panel", "chat": obj{"id": "999"}}})
	if !strings.Contains(legacyPanelReply.Text, "RelayPilot 控制中枢") {
		t.Fatalf("legacy panel alias should remain compatible: %#v", legacyPanelReply)
	}
	panelMarkup, _ := json.Marshal(panelReply.ReplyMarkup)
	if strings.Contains(panelReply.Text, "下一步") || strings.Contains(panelReply.Text, "任务结果") {
		t.Fatalf("panel should not expose next-step/task wording: text=%s", panelReply.Text)
	}
	if !strings.Contains(panelReply.Text, "最近操作") || !bytes.Contains(panelMarkup, []byte("rp:agents")) || !bytes.Contains(panelMarkup, []byte("rp:topology")) || !bytes.Contains(panelMarkup, []byte("rp:results")) || !bytes.Contains(panelMarkup, []byte("rp:update")) || !bytes.Contains(panelMarkup, []byte("节点列表")) || !bytes.Contains(panelMarkup, []byte("拓扑")) || !bytes.Contains(panelMarkup, []byte("最近操作")) || !bytes.Contains(panelMarkup, []byte("更新中心")) {
		t.Fatalf("panel should surface four primary entries: text=%s markup=%s", panelReply.Text, panelMarkup)
	}
	if strings.Contains(panelReply.Text, "常用操作") || strings.Contains(panelReply.Text, "命令菜单") || bytes.Contains(panelMarkup, []byte("GitHub")) || bytes.Contains(panelMarkup, []byte("刷新面板")) || bytes.Contains(panelMarkup, []byte("刷新节点详情")) || bytes.Contains(panelMarkup, []byte("链路检测")) || bytes.Contains(panelMarkup, []byte("rp:sync_all")) || bytes.Contains(panelMarkup, []byte("rp:probe_help")) {
		t.Fatalf("panel should avoid utility buttons and explanatory clutter: text=%s markup=%s", panelReply.Text, panelMarkup)
	}
	if bytes.Contains(panelMarkup, []byte("rp:guide")) || bytes.Contains(panelMarkup, []byte("操作向导")) {
		t.Fatalf("telegram hub panel should not expose operation guide as a main action, markup=%s", panelMarkup)
	}
	if bytes.Contains(panelMarkup, []byte("生成")) || bytes.Contains(panelMarkup, []byte("导出")) {
		t.Fatalf("telegram hub panel should stay status-focused, markup=%s", panelMarkup)
	}
	agentsReply := handleTelegramHubReply(state, obj{"update_id": 16, "callback_query": obj{"data": "rp:agents", "message": obj{"chat": obj{"id": "999"}}}})
	agentsMarkup, _ := json.Marshal(agentsReply.ReplyMarkup)
	if !strings.Contains(agentsReply.Text, "节点列表") || !bytes.Contains(agentsMarkup, []byte("刷新节点详情")) || !bytes.Contains(agentsMarkup, []byte("rp:sync_all")) || !bytes.Contains(agentsMarkup, []byte("rp:agent:transit-hk")) {
		t.Fatalf("agents page should expose node detail refresh: %#v markup=%s", agentsReply, agentsMarkup)
	}
	agentsCommandReply := handleTelegramHubReply(state, obj{"update_id": 29, "message": obj{"text": "/relaypilot_agents", "chat": obj{"id": "999"}}})
	agentsCommandMarkup, _ := json.Marshal(agentsCommandReply.ReplyMarkup)
	if !strings.Contains(agentsCommandReply.Text, "节点列表") || !bytes.Contains(agentsCommandMarkup, []byte("rp:agent:transit-hk")) || !bytes.Contains(agentsCommandMarkup, []byte("rp:sync_all")) {
		t.Fatalf("agents command should open the same button page: %#v markup=%s", agentsCommandReply, agentsCommandMarkup)
	}
	nodeDetail := handleTelegramHubReply(state, obj{"update_id": 18, "callback_query": obj{"data": "rp:agent:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	nodeDetailMarkup, _ := json.Marshal(nodeDetail.ReplyMarkup)
	for _, want := range []string{"ID：", "transit-hk", "角色：中转", "状态：在线", "网络：动态", "IP 8.8.*.*", "位置 中国香港", "详情：上次刷新"} {
		if !strings.Contains(nodeDetail.Text, want) {
			t.Fatalf("node detail missing %q: %#v", want, nodeDetail)
		}
	}
	if strings.Contains(nodeDetail.Text, "8.8.8.8") {
		t.Fatalf("node detail should mask IPs: %#v", nodeDetail)
	}
	for _, want := range [][]byte{
		[]byte("rp:agent:sync:transit-hk"),
		[]byte("rp:agent:doctor:transit-hk"),
		[]byte("rp:agent:related:transit-hk"),
		[]byte("rp:agent:results:transit-hk"),
		[]byte("rp:agent:retire:transit-hk"),
		[]byte("rp:agents"),
	} {
		if !bytes.Contains(nodeDetailMarkup, want) {
			t.Fatalf("node detail missing callback %s: markup=%s", want, nodeDetailMarkup)
		}
	}
	if bytes.Contains(nodeDetailMarkup, []byte("Endpoints")) || bytes.Contains(nodeDetailMarkup, []byte("更新节点")) || bytes.Contains(nodeDetailMarkup, []byte("rp:agent:endpoints")) || bytes.Contains(nodeDetailMarkup, []byte("rp:agent:update")) {
		t.Fatalf("node detail should not expose endpoint/update buttons: markup=%s", nodeDetailMarkup)
	}
	nodeSync := handleTelegramHubReply(state, obj{"update_id": 20, "callback_query": obj{"data": "rp:agent:sync:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(nodeSync.Text, "已下发节点详情刷新") {
		t.Fatalf("node sync reply = %#v", nodeSync)
	}
	nodeDoctor := handleTelegramHubReply(state, obj{"update_id": 21, "callback_query": obj{"data": "rp:agent:doctor:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(nodeDoctor.Text, "已下发 /doctor") || !strings.Contains(nodeDoctor.Text, "transit-hk") {
		t.Fatalf("node doctor reply = %#v", nodeDoctor)
	}
	nodeRelated := handleTelegramHubReply(state, obj{"update_id": 22, "callback_query": obj{"data": "rp:agent:related:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	nodeRelatedMarkup, _ := json.Marshal(nodeRelated.ReplyMarkup)
	if !strings.Contains(nodeRelated.Text, "关联节点") || !strings.Contains(nodeRelated.Text, "landing-hk") || !strings.Contains(nodeRelated.Text, "user:user-hk") || !bytes.Contains(nodeRelatedMarkup, []byte("rp:agent:landing-hk")) {
		t.Fatalf("node related reply = %#v markup=%s", nodeRelated, nodeRelatedMarkup)
	}
	nodeResults := handleTelegramHubReply(state, obj{"update_id": 23, "callback_query": obj{"data": "rp:agent:results:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(nodeResults.Text, "transit-hk") || strings.Contains(nodeResults.Text, "任务结果") {
		t.Fatalf("node results reply = %#v", nodeResults)
	}
	retireReply := handleTelegramHubReply(state, obj{"update_id": 24, "callback_query": obj{"data": "rp:agent:retire:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	retireMarkup, _ := json.Marshal(retireReply.ReplyMarkup)
	if !strings.Contains(retireReply.Text, "退役节点") || !strings.Contains(retireReply.Text, "确认后才执行") || !bytes.Contains(retireMarkup, []byte("rp:agent:retire-mode:uninstall:transit-hk")) {
		t.Fatalf("retire picker = %#v markup=%s", retireReply, retireMarkup)
	}
	retireConfirm := handleTelegramHubReply(state, obj{"update_id": 25, "callback_query": obj{"data": "rp:agent:retire-mode:uninstall:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	retireConfirmMarkup, _ := json.Marshal(retireConfirm.ReplyMarkup)
	if !strings.Contains(retireConfirm.Text, "确认退役") || !strings.Contains(retireConfirm.Text, "彻底卸载") || !bytes.Contains(retireConfirmMarkup, []byte("rp:agent:retire-confirm:uninstall:transit-hk")) || !bytes.Contains(retireConfirmMarkup, []byte("rp:agent:retire-preview:uninstall:transit-hk")) {
		t.Fatalf("retire confirmation = %#v markup=%s", retireConfirm, retireConfirmMarkup)
	}
	retirePreview := handleTelegramHubReply(state, obj{"update_id": 26, "callback_query": obj{"data": "rp:agent:retire-preview:uninstall:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	retirePreviewMarkup, _ := json.Marshal(retirePreview.ReplyMarkup)
	if !strings.Contains(retirePreview.Text, "退役预览") || !bytes.Contains(retirePreviewMarkup, []byte("rp:agent:retire-confirm:uninstall:transit-hk")) {
		t.Fatalf("retire preview = %#v markup=%s", retirePreview, retirePreviewMarkup)
	}
	retireExecute := handleTelegramHubReply(state, obj{"update_id": 27, "callback_query": obj{"data": "rp:agent:retire-confirm:uninstall:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(retireExecute.Text, "已下发远程退役") || !strings.Contains(retireExecute.Text, "transit-hk") {
		t.Fatalf("retire execute = %#v", retireExecute)
	}
	topologyReply := handleTelegramHubReply(state, obj{"update_id": 17, "callback_query": obj{"data": "rp:topology", "message": obj{"chat": obj{"id": "999"}}}})
	topologyMarkup, _ := json.Marshal(topologyReply.ReplyMarkup)
	if !strings.Contains(topologyReply.Text, "转发拓扑") || !bytes.Contains(topologyMarkup, []byte("链路检测")) || !bytes.Contains(topologyMarkup, []byte("rp:probe_help")) {
		t.Fatalf("topology page should expose link probe: %#v markup=%s", topologyReply, topologyMarkup)
	}
	probeHelp := handleTelegramHubReply(state, obj{"update_id": 8, "callback_query": obj{"data": "rp:probe_help", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(probeHelp.Text, "链路检测") || !strings.Contains(probeHelp.Text, "/relaypilot_probe") || !strings.Contains(probeHelp.Text, "不会启动后台监控") {
		t.Fatalf("probe help reply = %#v", probeHelp)
	}
	syncReply := handleTelegramHubReply(state, obj{"update_id": 7, "callback_query": obj{"data": "rp:sync_all", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(syncReply.Text, "已下发节点详情刷新") {
		t.Fatalf("sync reply = %#v", syncReply)
	}
	updateReply := handleTelegramHubReply(state, obj{"update_id": 5, "callback_query": obj{"data": "rp:update", "message": obj{"chat": obj{"id": "999"}}}})
	updateMarkup, _ := json.Marshal(updateReply.ReplyMarkup)
	if strings.Contains(updateReply.Text, "/relaypilot_up") || strings.Contains(updateReply.Text, "/relaypilot_update") || !strings.Contains(updateReply.Text, "默认：latest + 检查后重启") || !bytes.Contains(updateMarkup, []byte("rp:upd:agent")) {
		t.Fatalf("update center should stay button-first without command clutter: %#v", updateReply)
	}
	agentUpdateMenu := handleTelegramHubReply(state, obj{"update_id": 9, "callback_query": obj{"data": "rp:upd:agent", "message": obj{"chat": obj{"id": "999"}}}})
	agentUpdateMarkup, _ := json.Marshal(agentUpdateMenu.ReplyMarkup)
	if !strings.Contains(agentUpdateMenu.Text, "选择 Agent 更新范围") || !bytes.Contains(agentUpdateMarkup, []byte("rp:upd:agent:all")) || !bytes.Contains(agentUpdateMarkup, []byte("rp:upd:agent:transit-hk")) {
		t.Fatalf("agent update menu = %#v markup=%s", agentUpdateMenu, agentUpdateMarkup)
	}
	confirmUpdate := handleTelegramHubReply(state, obj{"update_id": 10, "callback_query": obj{"data": "rp:upd:agent:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	confirmMarkup, _ := json.Marshal(confirmUpdate.ReplyMarkup)
	if !strings.Contains(confirmUpdate.Text, "检查并更新") || !strings.Contains(confirmUpdate.Text, "目标：Agent：transit-hk") || !strings.Contains(confirmUpdate.Text, "目标版本：latest") || !strings.Contains(confirmUpdate.Text, "先检查目标版本") || strings.Contains(confirmUpdate.Text, "/relaypilot_up") || !bytes.Contains(confirmMarkup, []byte("rp:upd:run:transit-hk")) || !bytes.Contains(confirmMarkup, []byte("检查并更新/重启")) {
		t.Fatalf("confirm update should show target and remain button-first: %#v markup=%s", confirmUpdate, confirmMarkup)
	}
	hubConfirm := handleTelegramHubReply(state, obj{"update_id": 13, "callback_query": obj{"data": "rp:upd:hub", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(hubConfirm.Text, "目标：Hub") || !strings.Contains(hubConfirm.Text, "先检查当前/最新版本") || strings.Contains(hubConfirm.Text, "Agent 轮询领取") {
		t.Fatalf("hub confirm should show hub-specific target/effect: %#v", hubConfirm)
	}
	emptyState := filepath.Join(root, "empty-hub")
	if err := saveHubRegistry(emptyState, defaultHubRegistry()); err != nil {
		t.Fatal(err)
	}
	emptyAgentMenu := handleTelegramHubReply(emptyState, obj{"update_id": 14, "callback_query": obj{"data": "rp:upd:agent", "message": obj{"chat": obj{"id": "999"}}}})
	emptyAgentMarkup, _ := json.Marshal(emptyAgentMenu.ReplyMarkup)
	if !strings.Contains(emptyAgentMenu.Text, "当前没有已接入 Agent") || bytes.Contains(emptyAgentMarkup, []byte("rp:upd:agent:all")) || bytes.Contains(emptyAgentMarkup, []byte("Transit")) {
		t.Fatalf("empty agent update menu should not offer unusable ranges: %#v markup=%s", emptyAgentMenu, emptyAgentMarkup)
	}
	buttonUpdate := handleTelegramHubReply(state, obj{"update_id": 11, "callback_query": obj{"data": "rp:upd:run:transit-hk", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(buttonUpdate.Text, "已下发 RelayPilot 更新") || !strings.Contains(buttonUpdate.Text, "版本：latest") || !strings.Contains(buttonUpdate.Text, "重启服务：yes") {
		t.Fatalf("button update reply = %#v", buttonUpdate)
	}
	shortUpdate := handleTelegramHubUpdate(state, obj{"update_id": 6, "message": obj{"text": "/relaypilot_up transit-hk", "chat": obj{"id": "999"}}})
	if !strings.Contains(shortUpdate, "已下发 RelayPilot 更新") || !strings.Contains(shortUpdate, "版本：latest") || !strings.Contains(shortUpdate, "重启服务：yes") {
		t.Fatalf("short update reply = %s", shortUpdate)
	}
	unprefixedUpdate := handleTelegramHubUpdate(state, obj{"update_id": 12, "message": obj{"text": "/up transit-hk", "chat": obj{"id": "999"}}})
	if unprefixedUpdate != "" {
		t.Fatalf("unprefixed update should be ignored to avoid cross-service conflicts: %s", unprefixedUpdate)
	}
	queued := handleTelegramHubUpdate(state, obj{"update_id": 3, "message": obj{"text": "/relaypilot_status transit-hk", "chat": obj{"id": "999"}}})
	if !strings.Contains(queued, "已下发 /status") {
		t.Fatalf("queued = %s", queued)
	}
	batchID := extractBatchID(queued)
	if batchID == "" {
		t.Fatalf("missing batch id: %s", queued)
	}
	pending, err := loadPendingTGBatches(state)
	if err != nil {
		t.Fatal(err)
	}
	pendingBatch := asObj(asObj(pending["batches"])[batchID])
	if pendingBatch["chat_id"] != "999" || pendingBatch["origin_text"] != "/relaypilot_status transit-hk" {
		t.Fatalf("pending batch = %#v", pendingBatch)
	}
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	var task obj
	for _, candidate := range tasks {
		if candidate["agent_id"] == "transit-hk" && candidate["command"] == "status" {
			task = candidate
			break
		}
	}
	if len(task) == 0 {
		t.Fatalf("status task missing: %#v", tasks)
	}
	var updateTask obj
	for _, candidate := range tasks {
		if candidate["agent_id"] == "transit-hk" && candidate["command"] == "self_update" {
			updateTask = candidate
			break
		}
	}
	if len(updateTask) == 0 || !truthy(asObj(updateTask["payload"])["restart_services"]) || len(asList(updateTask["args"])) != 1 || str(asList(updateTask["args"])[0]) != "latest" {
		t.Fatalf("short update task missing or wrong: %#v", tasks)
	}
	var destructiveRetireTask obj
	for _, candidate := range tasks {
		if candidate["agent_id"] == "transit-hk" && candidate["command"] == "decommission_agent" && str(candidate["origin_text"]) == "/decommission transit-hk --mode uninstall --confirm transit-hk" {
			destructiveRetireTask = candidate
			break
		}
	}
	if len(destructiveRetireTask) == 0 || truthy(asObj(destructiveRetireTask["payload"])["dry_run"]) {
		t.Fatalf("confirmed retire task missing or not destructive: %#v", tasks)
	}
	if _, err := completeHubTask(state, str(task["id"]), "transit-hk", obj{"success": true, "command": "status", "text": "agent ok"}); err != nil {
		t.Fatal(err)
	}
	ready, err := collectReadyPendingTGBatches(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0]["chat_id"] != "999" || !strings.Contains(str(ready[0]["text"]), "agent ok") {
		t.Fatalf("ready batches = %#v", ready)
	}
	readyAgain, err := collectReadyPendingTGBatches(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(readyAgain) != 0 {
		t.Fatalf("ready batch sent twice: %#v", readyAgain)
	}
	if path, err := configureTelegram(state, "123456:SECRET_TOKEN", "123", "https://api.telegram.example/", true); err != nil {
		t.Fatal(err)
	} else if path != telegramConfigPath(state) {
		t.Fatalf("telegram config path = %s", path)
	}
	status := telegramStatus(state)
	if status["configured"] != true || status["bot_token"] != "1234...OKEN" || status["api_base"] != "https://api.telegram.example" {
		t.Fatalf("telegram status = %#v", status)
	}
	cmdText := formatTelegramCommands(false, true)
	if strings.TrimSpace(cmdText) != "/relaypilot — Open RelayPilot Hub panel" || strings.Contains(cmdText, "/relaypilot_panel") || strings.Contains(cmdText, "/relaypilot_status") || strings.Contains(cmdText, "/relaypilot_up") || strings.Contains(cmdText, "/relaypilot_topology") || strings.Contains(cmdText, "/relaypilot_decommission") {
		t.Fatalf("hub commands should expose only the RelayPilot panel entry: %s", cmdText)
	}
	hubCmdJSON := selectedTelegramCommands(true)
	if len(hubCmdJSON) != 1 || str(hubCmdJSON[0]["command"]) != "relaypilot" {
		t.Fatalf("hub command menu should stay /relaypilot-only: %#v", hubCmdJSON)
	}
	cmdJSON := selectedTelegramCommands(false)
	if len(cmdJSON) == 0 || str(cmdJSON[0]["command"]) != "relaypilot_help" {
		t.Fatalf("commands json = %#v", cmdJSON)
	}
	scope, err := parseBotCommandScope("chat_member:-100:42")
	if err != nil {
		t.Fatal(err)
	}
	if scope["type"] != "chat_member" || int64Value(scope["user_id"]) != 42 {
		t.Fatalf("scope = %#v", scope)
	}
	dryRegister, err := registerTelegramCommands(state, "default", "zh", true, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := asObj(dryRegister["payload"])
	commandsPayload := str(payload["commands"])
	if dryRegister["method"] != "setMyCommands" || strings.Contains(str(dryRegister["url"]), "SECRET_TOKEN") || strings.Contains(commandsPayload, "decommission") || strings.Contains(commandsPayload, "topology") || strings.Contains(commandsPayload, "relaypilot_up") || strings.Contains(commandsPayload, "relaypilot_status") || strings.Contains(commandsPayload, "relaypilot_panel") || !strings.Contains(commandsPayload, "relaypilot") {
		t.Fatalf("dry register = %#v", dryRegister)
	}
	drySend, err := sendTelegramMessageCLI(state, "hello", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if asObj(drySend["payload"])["text"] != "hello" {
		t.Fatalf("dry send = %#v", drySend)
	}
	ipPayload, err := telegramAPICall(state, "sendMessage", obj{
		"chat_id": "123",
		"text":    "node 172.81.102.137",
		"reply_markup": tgKeyboard([]any{
			tgButton("inspect 172.81.102.137", "rp:agent:172.81.102.137"),
		}),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	ipWire, _ := json.Marshal(asObj(ipPayload["payload"]))
	if bytes.Contains(ipWire, []byte("172.81.102.137")) {
		t.Fatalf("telegram wire payload leaked raw IPv4: %s", ipWire)
	}
	if !bytes.Contains(ipWire, []byte("172.81.*.*")) || !bytes.Contains(ipWire, []byte("tgcb:")) {
		t.Fatalf("telegram wire payload should mask text and tokenize callback data: %s", ipWire)
	}
	var replyMarkup obj
	if err := json.Unmarshal([]byte(str(asObj(ipPayload["payload"])["reply_markup"])), &replyMarkup); err != nil {
		t.Fatal(err)
	}
	token := str(asObj(asList(asList(replyMarkup["inline_keyboard"])[0])[0])["callback_data"])
	if !strings.HasPrefix(token, "tgcb:") {
		t.Fatalf("callback data should be tokenized: %s", token)
	}
	if got := resolveTelegramCallbackData(state, token); got != "rp:agent:172.81.102.137" {
		t.Fatalf("callback token should resolve locally, got %q", got)
	}
	tokenReply := handleTelegramHubReply(state, obj{"update_id": 19, "callback_query": obj{"data": token, "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(tokenReply.Text, "IP Named Transit") {
		t.Fatalf("token callback should resolve locally: %#v", tokenReply)
	}
	tokenWire := telegramWirePayload(sanitizeTelegramPayload(obj{"_state_dir": state}, obj{"text": tokenReply.Text, "reply_markup": tokenReply.ReplyMarkup}))
	tokenWireJSON, _ := json.Marshal(tokenWire)
	if bytes.Contains(tokenWireJSON, []byte("172.81.102.137")) || !bytes.Contains(tokenWireJSON, []byte("172.81.*.*")) || !bytes.Contains(tokenWireJSON, []byte("tgcb:")) {
		t.Fatalf("resolved token reply should be sanitized before Telegram send: %s", tokenWireJSON)
	}

	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	err = sendTelegramMessage(obj{"api_base": srv.URL, "bot_token": "token", "chat_id": "123", "enabled": true}, strings.Repeat("界", 2000), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bottoken/sendMessage" {
		t.Fatalf("path = %s", gotPath)
	}
	form, err := url.ParseQuery(gotBody)
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("chat_id") != "123" || len([]byte(form.Get("text"))) > maxTelegramTextBytes {
		t.Fatalf("body = len %d text-bytes %d %s", len(gotBody), len([]byte(form.Get("text"))), gotBody[:minInt(len(gotBody), 80)])
	}
}

func TestHubAgentTokenLifecycle(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{"transit-hk": obj{"kind": agentRegistrationKind, "version": version, "id": "transit-hk", "role": "transit", "name": "HK Transit", "transport": "poll"}}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	issued, err := issueHubAgentToken(state, "transit-hk", "first-token")
	if err != nil {
		t.Fatal(err)
	}
	if issued["token"] != "first-token" {
		t.Fatalf("issued = %#v", issued)
	}
	ts := strconvNow()
	sig := mustSign(t, "first-token", "GET", "/api/agents/transit-hk/tasks?limit=1", nil, ts, "nonce-a", "transit-hk")
	if !verifySignature(state, "transit-hk", "GET", "/api/agents/transit-hk/tasks?limit=1", nil, ts, "nonce-a", sig) {
		t.Fatal("issued token did not verify")
	}
	tokens, err := listHubAgentTokens(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || str(tokens[0]["agent_id"]) != "transit-hk" || tokens[0]["has_token"] != true {
		t.Fatalf("tokens = %#v", tokens)
	}
	if strings.Contains(formatHubAgentTokensText(tokens), "first-token") {
		t.Fatalf("token text leaked secret: %s", formatHubAgentTokensText(tokens))
	}
	rotated, err := rotateHubAgentToken(state, "transit-hk", "second-token")
	if err != nil {
		t.Fatal(err)
	}
	if rotated["rotated"] != true || rotated["token"] != "second-token" {
		t.Fatalf("rotated = %#v", rotated)
	}
	if _, err := revokeHubAgentToken(state, "transit-hk"); err != nil {
		t.Fatal(err)
	}
	tokens, err = listHubAgentTokens(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("tokens after revoke = %#v", tokens)
	}
}

func mustSign(t *testing.T, token, method, path string, body []byte, timestamp, nonce, agentID string) string {
	t.Helper()
	sig, err := signAgentRequest(token, method, path, body, timestamp, nonce, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestRecoverStaleHubTasksRequeuesThenFails(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	agent := obj{"kind": agentRegistrationKind, "version": version, "id": "transit-hk", "role": "transit", "name": "HK Transit", "transport": "poll"}
	reg["agents"] = obj{"transit-hk": agent}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := createHubTask(state, agent, "status", nil, "batch-a", "/status transit-hk"); err != nil {
		t.Fatal(err)
	}

	leased, err := leaseHubAgentTasks(state, "transit-hk", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || str(leased[0]["status"]) != "running" {
		t.Fatalf("leased = %#v", leased)
	}
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	task := tasks[0]
	path := str(task["path"])
	delete(task, "path")
	task["leased_at"] = now() - 10
	if err := writeJSON(path, task, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := recoverStaleHubTasks(state, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if recovered["requeued"] != 1 {
		t.Fatalf("recover = %#v", recovered)
	}
	tasks, err = listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if str(tasks[0]["status"]) != "queued" || str(tasks[0]["requeue_reason"]) != "lease_timeout" {
		t.Fatalf("task after requeue = %#v", tasks[0])
	}

	leased, err = leaseHubAgentTasks(state, "transit-hk", 1)
	if err != nil {
		t.Fatal(err)
	}
	if int64Value(leased[0]["lease_count"]) != 2 {
		t.Fatalf("second lease = %#v", leased[0])
	}
	tasks, err = listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	task = tasks[0]
	path = str(task["path"])
	delete(task, "path")
	task["leased_at"] = now() - 10
	if err := writeJSON(path, task, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err = recoverStaleHubTasks(state, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if recovered["failed"] != 1 {
		t.Fatalf("recover failed = %#v", recovered)
	}
	tasks, err = listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if str(tasks[0]["status"]) != "failed" || !strings.Contains(str(asObj(tasks[0]["result"])["error"]), "lease expired") {
		t.Fatalf("task after fail = %#v", tasks[0])
	}
}

func TestGoOfflineAlertCallbackRemoveAndObserve(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "hub")
	nowTs := now()
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"landing-hk": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "landing-hk",
			"role":      "landing",
			"name":      "HK Landing",
			"transport": "poll",
			"last_seen": nowTs - offlineAlertSeconds - 10,
			"topology":  obj{"links": []any{}, "endpoints": []any{}, "inbounds": []any{}},
			"health":    obj{"status": "ok"},
		},
		"transit-sg": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-sg",
			"role":      "transit",
			"name":      "SG Transit",
			"transport": "poll",
			"last_seen": nowTs - offlineAlertSeconds - 20,
			"topology":  obj{"links": []any{}, "endpoints": []any{}, "inbounds": []any{}},
			"health":    obj{"status": "ok"},
		},
	}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := createHubTask(state, asObj(asObj(reg["agents"])["landing-hk"]), "status", nil, "batch-a", "/status landing-hk"); err != nil {
		t.Fatal(err)
	}

	dry, err := scanOfflineAgentAlerts(state, offlineAlertSeconds, offlineAlertSnoozeSecs, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(asList(dry["sent"])) != 2 {
		t.Fatalf("dry sent = %#v", dry)
	}
	if _, err := os.Stat(hubAlertsPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run should not persist alerts: %v", err)
	}

	alertsData := defaultHubAlerts()
	alertsData["alerts"] = obj{
		"landing-hk": obj{
			"agent_id":            "landing-hk",
			"status":              "alerted",
			"agent":               summarizeAgent(asObj(asObj(reg["agents"])["landing-hk"])),
			"offline_age_seconds": offlineAlertSeconds + 10,
			"remove_token":        "rm-token",
			"observe_token":       "obs-token",
			"updated_at":          nowTs,
		},
		"transit-sg": obj{
			"agent_id":            "transit-sg",
			"status":              "alerted",
			"agent":               summarizeAgent(asObj(asObj(reg["agents"])["transit-sg"])),
			"offline_age_seconds": offlineAlertSeconds + 20,
			"remove_token":        "rm-token-2",
			"observe_token":       "obs-token-2",
			"updated_at":          nowTs,
		},
	}
	if err := saveHubAlertsData(state, alertsData); err != nil {
		t.Fatal(err)
	}
	observed := handleTelegramHubUpdate(state, obj{"update_id": 3, "callback_query": obj{"data": "rp:obs:obs-token-2"}})
	if !strings.Contains(observed, "继续观察节点 transit-sg") {
		t.Fatalf("observe reply = %s", observed)
	}
	removed := handleTelegramHubUpdate(state, obj{"update_id": 4, "callback_query": obj{"data": "rp:rm:rm-token"}})
	if !strings.Contains(removed, "已删除节点 landing-hk") {
		t.Fatalf("remove reply = %s", removed)
	}
	agents, err := listHubAgents(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		if str(agent["id"]) == "landing-hk" {
			t.Fatalf("landing-hk still active: %#v", agents)
		}
	}
	removedAgents, err := listRemovedAgents(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(removedAgents) != 1 || str(removedAgents[0]["id"]) != "landing-hk" {
		t.Fatalf("removed agents = %#v", removedAgents)
	}
	tasks, err := listHubTasks(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || str(tasks[0]["status"]) != "cancelled" {
		t.Fatalf("tasks after remove = %#v", tasks)
	}
	alerts, err := listHubAlerts(state)
	if err != nil {
		t.Fatal(err)
	}
	statusByID := map[string]string{}
	for _, alert := range alerts {
		statusByID[str(alert["agent_id"])] = str(alert["status"])
	}
	if statusByID["landing-hk"] != "removed" || statusByID["transit-sg"] != "observing" {
		t.Fatalf("alert statuses = %#v", statusByID)
	}
}

func TestResourceGuardsLimitJSONHTTPAndTaskBatch(t *testing.T) {
	root := t.TempDir()
	largeJSON := filepath.Join(root, "large.json")
	if err := os.WriteFile(largeJSON, []byte(`{"x":"`+strings.Repeat("a", maxJSONFileBytes)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadJSON(largeJSON); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("large json error = %v", err)
	}

	poller := newAgentPoller("http://127.0.0.1", "agent-a", "token", "transit", root, "", maxHubTasksPerPoll+100, time.Second, 0)
	if poller.maxTasks != maxHubTasksPerPoll {
		t.Fatalf("maxTasks not clamped: %d", poller.maxTasks)
	}

	srv := httptest.NewServer(&hubServer{stateDir: root, quiet: true})
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/agents/agent-a/heartbeat", "application/json", strings.NewReader(strings.Repeat("x", maxHTTPBodyBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", resp.StatusCode)
	}
}

func TestAgentPollerStaticHeartbeatIsMinimal(t *testing.T) {
	state := t.TempDir()
	registration, err := buildHubAgentRegistration("agent-a", "landing", "Agent A", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveHubAgentRegistration(state, registration); err != nil {
		t.Fatal(err)
	}
	if _, err := issueHubAgentToken(state, "agent-a", "secret"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&hubServer{stateDir: state, quiet: true})
	defer srv.Close()

	probes := 0
	poller := newAgentPoller(srv.URL, "agent-a", "secret", "landing", state, "", 0, time.Second, 0)
	poller.ipMode = "static"
	poller.publicIPInterval = time.Hour
	poller.publicIPProbe = func(time.Duration) (string, error) {
		probes++
		return "198.51.100.23", nil
	}
	for i := 0; i < 2; i++ {
		if _, err := poller.pollOnce(); err != nil {
			t.Fatal(err)
		}
	}
	if probes != 0 {
		t.Fatalf("static heartbeat should not probe public IP, got %d", probes)
	}
	agents, err := listHubAgents(state)
	if err != nil {
		t.Fatal(err)
	}
	network := asObj(agents[0]["network"])
	if network["reported_at"] == nil {
		t.Fatalf("network = %#v", network)
	}
	if network["ip_mode"] != nil || network["public_ip"] != nil {
		t.Fatalf("minimal heartbeat leaked network fields: %#v", network)
	}
	if net.ParseIP(str(network["observed_ip"])) == nil {
		t.Fatalf("observed_ip = %#v, network = %#v", network["observed_ip"], network)
	}
	if listing := formatAgentsText(agents); strings.Contains(listing, "动态IP") {
		t.Fatalf("agent listing should not advertise dynamic IP from heartbeat: %q", listing)
	}
}

func TestUpdateAgentIPModePreservesEnrollment(t *testing.T) {
	state := t.TempDir()
	path := agentEnrollmentPath(state)
	if err := writeJSON(path, obj{
		"hub_url":     "https://hub.example:8443",
		"agent_id":    "agent-a",
		"role":        "transit",
		"token_file":  filepath.Join(state, "agent-token"),
		"ca_cert":     filepath.Join(state, "hub-ca.crt"),
		"client_cert": filepath.Join(state, "agent.crt"),
		"client_key":  filepath.Join(state, "agent.key"),
		"created_at":  int64(123),
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := updateAgentIPMode(state, "dynamic", 1800)
	if err != nil {
		t.Fatal(err)
	}
	if str(res["ip_mode"]) != "dynamic" || int64Value(res["public_ip_interval_seconds"]) != 1800 {
		t.Fatalf("result = %#v", res)
	}
	cfg, err := loadAgentEnrollment(state)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IPMode != "dynamic" || cfg.PublicIPIntervalSeconds != 1800 || cfg.HubURL != "https://hub.example:8443" {
		t.Fatalf("updated enrollment = %#v", cfg)
	}
}

func TestStaticHeartbeatNetworkPayloadIsMinimal(t *testing.T) {
	poller := newAgentPoller("http://127.0.0.1", "agent-a", "secret", "landing", t.TempDir(), "", 0, time.Second, 0)
	poller.ipMode = "static"
	network := poller.network()
	if len(network) != 1 || network["reported_at"] == nil {
		t.Fatalf("network payload = %#v", network)
	}
}

func TestStaticHeartbeatDoesNotCallPublicIPProbe(t *testing.T) {
	poller := newAgentPoller("http://127.0.0.1", "agent-a", "secret", "landing", t.TempDir(), "", 0, 30*time.Second, 0)
	poller.ipMode = "static"
	called := false
	poller.publicIPProbe = func(timeout time.Duration) (string, error) {
		called = true
		return "198.51.100.42", nil
	}
	_ = poller.network()
	if called {
		t.Fatal("static heartbeat network path should not invoke public IP probe")
	}
}

func TestDynamicHeartbeatReportsAndCachesPublicIP(t *testing.T) {
	poller := newAgentPoller("http://127.0.0.1", "agent-a", "secret", "landing", t.TempDir(), "", 0, 30*time.Second, 0)
	poller.ipMode = "dynamic"
	poller.publicIPInterval = time.Hour
	calls := 0
	poller.publicIPProbe = func(timeout time.Duration) (string, error) {
		calls++
		return "8.8.8.8", nil
	}
	network := poller.network()
	if str(network["ip_mode"]) != "dynamic" || str(network["public_ip"]) != "8.8.8.8" || int64Value(network["public_ip_checked_at"]) == 0 || network["public_ip_error"] != nil {
		t.Fatalf("dynamic network payload = %#v", network)
	}
	again := poller.network()
	if calls != 1 || str(again["public_ip"]) != "8.8.8.8" {
		t.Fatalf("public IP probe should be cached, calls=%d again=%#v", calls, again)
	}
}

func TestAgentPollerCachesTopologySnapshot(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds": []any{obj{"type": "vless", "tag": "in-1"}},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	poller := newAgentPoller("http://127.0.0.1", "agent-a", "token", "transit", state, conf, 5, time.Second, 3600)
	topo1, _ := poller.topology()
	if len(asList(topo1["inbounds"])) != 1 {
		t.Fatalf("topo1 = %#v", topo1)
	}
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds": []any{obj{"type": "vless", "tag": "in-1"}, obj{"type": "vless", "tag": "in-2"}},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	topo2, _ := poller.topology()
	if len(asList(topo2["inbounds"])) != 1 {
		t.Fatalf("topology cache missed: %#v", topo2)
	}
	poller.topologyExpires = time.Now().Add(-time.Second)
	topo3, _ := poller.topology()
	if len(asList(topo3["inbounds"])) != 2 {
		t.Fatalf("topology cache did not refresh: %#v", topo3)
	}
}

func strconvNow() string { return strconv.FormatInt(now(), 10) }

func TestMakeAgentRegistrationWithSnapshotAndLabels(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	endpoint := obj{
		"kind":        endpointKind,
		"version":     version,
		"name":        "hk",
		"protocol":    "shadowsocks",
		"tag":         "landing-hk-ss",
		"server":      "203.0.113.10",
		"server_port": 443,
		"method":      "2022-blake3-aes-128-gcm",
		"password":    "YWFhYWFhYWFhYWFhYWFhYQ==",
		"network":     "tcp,udp",
	}
	if err := writeJSON(filepath.Join(state, "endpoints", "hk.json"), endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "00.json"), obj{
		"inbounds": []any{
			obj{"type": "vless", "tag": "vless-in", "listen": "::", "listen_port": 443, "users": []any{obj{"name": "alice", "uuid": "u"}}},
		},
		"route": obj{"rules": []any{
			obj{"auth_user": []any{"alice"}, "outbound": "landing-hk-ss", "action": "route"},
		}},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := makeAgentRegistration("transit-hk", "transit", "", "", "region=hk, tier = edge", "poll", state, conf, false)
	if err != nil {
		t.Fatal(err)
	}
	if reg["name"] != "transit-hk" || reg["transport"] != "poll" {
		t.Fatalf("bad registration defaults: %#v", reg)
	}
	labels := asObj(reg["labels"])
	if labels["region"] != "hk" || labels["tier"] != "edge" {
		t.Fatalf("labels = %#v", labels)
	}
	topo := asObj(reg["topology"])
	if len(asList(topo["endpoints"])) != 1 || len(asList(topo["inbounds"])) != 1 || len(asList(topo["links"])) != 1 {
		t.Fatalf("topology = %#v", topo)
	}
	link := asObj(asList(topo["links"])[0])
	if link["auth_user"] != "alice" || link["endpoint_name"] != "hk" {
		t.Fatalf("link = %#v", link)
	}
	if asObj(reg["health"])["status"] != "ok" {
		t.Fatalf("health = %#v", reg["health"])
	}
}

func TestCollectTopologyIncludesRealityClientWithoutPrivateKey(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	conf := filepath.Join(root, "conf")
	if _, err := ensureTransitReality(conf, state, "::", 443, "vless-in", "addons.mozilla.org", "addons.mozilla.org", 443, "", "0123456789abcdef", "1m", false); err != nil {
		t.Fatal(err)
	}
	topo := collectTopology("transit", state, conf)
	realityClient := asObj(topo["reality_client"])
	if realityClient["server_name"] != "addons.mozilla.org" || realityClient["public_key"] == "" || realityClient["short_id"] == "" {
		t.Fatalf("reality_client = %#v", realityClient)
	}
	if realityClient["private_key"] != nil {
		t.Fatalf("reality_client leaked private key: %#v", realityClient)
	}
}

func TestMakeAgentRegistrationNoSnapshotAndBadLabels(t *testing.T) {
	reg, err := makeAgentRegistration("landing-1", "landing", "Landing 1", "http://127.0.0.1:8080", "", "poll", t.TempDir(), "/missing", true)
	if err != nil {
		t.Fatal(err)
	}
	topo := asObj(reg["topology"])
	if len(asList(topo["links"])) != 0 || len(asList(topo["endpoints"])) != 0 || len(asList(topo["inbounds"])) != 0 {
		t.Fatalf("no-snapshot topology = %#v", topo)
	}
	if asObj(reg["health"])["status"] != "ok" {
		t.Fatalf("health = %#v", reg["health"])
	}
	if reg["endpoint"] != "http://127.0.0.1:8080" {
		t.Fatalf("endpoint = %#v", reg["endpoint"])
	}
	if _, err := parseLabels("region"); err == nil {
		t.Fatal("bad label accepted")
	}
}

func TestRenderLandingImportAndBindTransit(t *testing.T) {
	root := t.TempDir()
	fixedPassword := "YWFhYWFhYWFhYWFhYWFhYQ=="
	endpoint, config, err := renderLandingSS("hk", "203.0.113.10", "::", 2443, 443, "2022-blake3-aes-128-gcm", fixedPassword, "tcp,udp", "ss-in", "landing-hk-ss")
	if err != nil {
		t.Fatal(err)
	}
	if config["route"].(obj)["final"] != "direct" {
		t.Fatalf("bad landing config: %#v", config)
	}
	endpointPath := filepath.Join(root, "hk.json")
	if err := writeJSON(endpointPath, endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	dest, err := importEndpoint(endpointPath, state)
	if err != nil {
		t.Fatal(err)
	}
	if dest != filepath.Join(state, "endpoints", "hk.json") {
		t.Fatalf("dest = %s", dest)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("endpoint mode = %o", info.Mode().Perm())
	}

	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00-inbounds.json"), obj{"inbounds": []any{obj{"type": "vless", "tag": "vless-in", "users": []any{obj{"name": "direct", "uuid": "11111111-1111-4111-8111-111111111111", "flow": "xtls-rprx-vision"}}}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "01-outbounds.json"), obj{"outbounds": []any{obj{"type": "direct", "tag": "direct"}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "02-route.json"), obj{"route": obj{"rules": []any{obj{"domain_suffix": []any{"example.com"}, "outbound": "direct", "action": "route"}}, "final": "direct"}}, 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := bindTransit(conf, endpoint, "vless-in", "hk", "22222222-2222-4222-8222-222222222222", "xtls-rprx-vision", true, state, true)
	if err != nil {
		t.Fatal(err)
	}
	if summary["auth_user"] != "hk" || summary["outbound_tag"] != "landing-hk-ss" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := os.Stat(str(summary["backup"])); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if _, err := bindTransit(conf, endpoint, "vless-in", "hk", "22222222-2222-4222-8222-222222222222", "xtls-rprx-vision", true, state, true); err != nil {
		t.Fatal(err)
	}
	inbounds, _ := loadJSON(filepath.Join(conf, "00-inbounds.json"))
	inbound := asObj(asList(inbounds["inbounds"])[0])
	users := asList(inbound["users"])
	seenHK := 0
	for _, raw := range users {
		if str(asObj(raw)["name"]) == "hk" {
			seenHK++
		}
	}
	if seenHK != 1 {
		t.Fatalf("hk user count = %d users=%#v", seenHK, users)
	}
	outbounds, _ := loadJSON(filepath.Join(conf, "01-outbounds.json"))
	seenOutbound := 0
	for _, raw := range asList(outbounds["outbounds"]) {
		outbound := asObj(raw)
		if str(outbound["tag"]) == "landing-hk-ss" {
			seenOutbound++
			if outbound["network"] != nil {
				t.Fatalf("dual-network shadowsocks outbound should omit network: %#v", outbound)
			}
		}
	}
	if seenOutbound != 1 {
		t.Fatalf("outbound count = %d", seenOutbound)
	}
	routeFile, _ := loadJSON(filepath.Join(conf, "02-route.json"))
	rules := asList(asObj(routeFile["route"])["rules"])
	first := asObj(rules[0])
	if !listContains(first["auth_user"], "hk") || first["outbound"] != "landing-hk-ss" {
		t.Fatalf("first rule = %#v", first)
	}
	if str(asObj(rules[1])["outbound"]) != "direct" {
		t.Fatalf("unknown rule not preserved: %#v", rules)
	}
}

func TestRenderLandingSOCKSAndBindTransit(t *testing.T) {
	root := t.TempDir()
	endpoint, config, err := renderLandingSOCKS("la", "198.51.100.20", "::", 1080, 2080, "sub2api", "secret-pass", "socks-in", "landing-la-socks")
	if err != nil {
		t.Fatal(err)
	}
	inbound := asObj(asList(config["inbounds"])[0])
	if inbound["type"] != "socks" || inbound["listen_port"] != 1080 {
		t.Fatalf("bad landing socks config: %#v", config)
	}
	users := asList(inbound["users"])
	if len(users) != 1 || asObj(users[0])["username"] != "sub2api" {
		t.Fatalf("bad socks users: %#v", users)
	}
	endpointPath := filepath.Join(root, "la.json")
	if err := writeJSON(endpointPath, endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if _, err := importEndpoint(endpointPath, state); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(root, "conf")
	if err := writeJSON(filepath.Join(conf, "00-inbounds.json"), obj{"inbounds": []any{obj{"type": "vless", "tag": "vless-in", "users": []any{}}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "01-outbounds.json"), obj{"outbounds": []any{obj{"type": "direct", "tag": "direct"}}}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(conf, "02-route.json"), obj{"route": obj{"rules": []any{}, "final": "direct"}}, 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := bindTransit(conf, endpoint, "vless-in", "la", "55555555-5555-4555-8555-555555555555", "xtls-rprx-vision", true, state, true)
	if err != nil {
		t.Fatal(err)
	}
	if summary["outbound_tag"] != "landing-la-socks" {
		t.Fatalf("summary = %#v", summary)
	}
	outbounds, _ := loadJSON(filepath.Join(conf, "01-outbounds.json"))
	found := false
	for _, raw := range asList(outbounds["outbounds"]) {
		outbound := asObj(raw)
		if str(outbound["tag"]) == "landing-la-socks" {
			found = true
			if outbound["type"] != "socks" || outbound["version"] != "5" || outbound["username"] != "sub2api" || outbound["password"] != "secret-pass" {
				t.Fatalf("bad socks outbound: %#v", outbound)
			}
		}
	}
	if !found {
		t.Fatalf("socks outbound missing: %#v", outbounds)
	}
}

func TestRenderLandingSOCKSRequiresCredentialPair(t *testing.T) {
	if _, _, err := renderLandingSOCKS("la", "198.51.100.20", "::", 1080, 1080, "sub2api", "", "socks-in", "landing-la-socks"); err == nil {
		t.Fatal("expected credential pair validation error")
	}
}

func TestHubExportTransitClientUsesRealitySnapshotAndPublicEntry(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
			"network": obj{
				"public_entries": []any{
					obj{"use": "reality", "name": "main", "host": "edge.example", "public_port": 443},
				},
			},
			"topology": obj{
				"inbounds": []any{
					obj{"tag": "vless-in", "type": "vless", "listen": "::", "listen_port": 443, "users": []any{
						obj{"name": "alice", "uuid": "11111111-1111-4111-8111-111111111111", "flow": "xtls-rprx-vision"},
						obj{"name": "bob", "uuid": "22222222-2222-4222-8222-222222222222", "flow": "xtls-rprx-vision"},
					}},
				},
				"reality_client": obj{
					"inbound_tag": "vless-in",
					"listen":      "::",
					"listen_port": 443,
					"server_name": "addons.mozilla.org",
					"public_key":  "pubkey123",
					"short_id":    "abcd1234",
				},
			},
			"sync_at": now(),
		},
	}
	if err := saveHubRegistry(hubState, reg); err != nil {
		t.Fatal(err)
	}
	exported, err := hubExportTransitClient(hubState, "transit-la", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if exported["server"] != "edge.example" || int64Value(exported["server_port"]) != 443 {
		t.Fatalf("server export = %#v", exported)
	}
	if exported["auth_user"] != "bob" || exported["public_key"] != "pubkey123" || exported["short_id"] != "abcd1234" {
		t.Fatalf("bad export = %#v", exported)
	}
	profile := asObj(exported["profile"])
	if profile["uuid"] != "22222222-2222-4222-8222-222222222222" || profile["server_name"] != "addons.mozilla.org" {
		t.Fatalf("profile = %#v", profile)
	}
	cache := asObj(exported["cache"])
	if cache["source"] != "hub-registry" || cache["synced_at"] == nil {
		t.Fatalf("cache = %#v", cache)
	}
}

func TestHubExportLandingConfigUsesSecureCache(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	endpoint, err := validateEndpoint(obj{
		"kind":          endpointKind,
		"version":       version,
		"name":          "la-direct",
		"protocol":      "socks",
		"server":        "198.51.100.20",
		"server_port":   2080,
		"socks_version": "5",
		"username":      "sub2api",
		"password":      "secret-pass",
		"tag":           "landing-la-direct-socks",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeHubLandingExport(hubState, "landing-la", endpoint, nil); err != nil {
		t.Fatal(err)
	}
	exported, err := hubExportLandingConfig(hubState, "landing-la", "la-direct")
	if err != nil {
		t.Fatal(err)
	}
	if exported["protocol"] != "socks" || exported["server"] != "198.51.100.20" {
		t.Fatalf("exported = %#v", exported)
	}
	cfg := asObj(exported["config"])
	if cfg["username"] != "sub2api" || cfg["password"] != "secret-pass" {
		t.Fatalf("config = %#v", cfg)
	}
	cache := asObj(exported["cache"])
	if cache["source"] != "hub-export-file" || cache["synced_at"] == nil {
		t.Fatalf("cache = %#v", cache)
	}
}

func TestFormatAgentsTextShowsCardRows(t *testing.T) {
	n := now()
	agents := []obj{
		obj{"id": "landing-jp", "role": "landing", "name": "JP Landing", "last_seen": n, "updated_at": n, "sync_at": n - 3*60, "network": obj{"observed_ip": "203.0.113.10", "location": obj{"ip": "203.0.113.10", "label": "日本·东京"}}},
		obj{"id": "transit-us", "role": "transit", "name": "US Transit", "last_seen": n, "updated_at": n, "sync_at": n - 5*60, "network": obj{"ip_mode": "dynamic", "public_ip": "8.8.8.8", "location": obj{"ip": "8.8.8.8", "label": "美国·洛杉矶"}}},
	}
	text := formatAgentsText(agents)
	for _, want := range []string{
		"📡 节点列表：2",
		"🟢 🛬 landing-jp · 落地 · JP Landing",
		"   静态 · IP 203.0.*.* · 位置 日本·东京 · 上次刷新 3 分钟前",
		"🟢 🛫 transit-us · 中转 · US Transit",
		"   动态 · IP 8.8.*.* · 位置 美国·洛杉矶 · 上次刷新 5 分钟前",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "203.0.113.10") || strings.Contains(text, "8.8.8.8") {
		t.Fatalf("agent list should mask IPs:\n%s", text)
	}
}

func TestUpdateHeartbeatStoresGeoLocation(t *testing.T) {
	oldResolver := hubIPLocationResolver
	defer func() { hubIPLocationResolver = oldResolver }()
	calls := 0
	hubIPLocationResolver = func(ip string, timeout time.Duration) (obj, error) {
		calls++
		if ip != "8.8.8.8" {
			t.Fatalf("lookup ip = %s", ip)
		}
		return obj{"country_code": "US", "country_name": "United States", "city": "Los Angeles", "source": "test"}, nil
	}

	state := t.TempDir()
	reg := defaultHubRegistry()
	reg["agents"] = obj{"landing-la": obj{"kind": agentRegistrationKind, "version": version, "id": "landing-la", "role": "landing", "name": "LA Landing", "transport": "poll"}}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	agent, err := updateHeartbeat(state, "landing-la", nil, nil, obj{"public_ip": "8.8.8.8"})
	if err != nil {
		t.Fatal(err)
	}
	location := asObj(asObj(agent["network"])["location"])
	if str(location["label"]) != "美国·洛杉矶" || str(location["ip"]) != "8.8.8.8" || int64Value(location["checked_at"]) == 0 {
		t.Fatalf("location = %#v", location)
	}
	if text := formatAgentsText([]obj{agent}); !strings.Contains(text, "位置 美国·洛杉矶") {
		t.Fatalf("agent list missing location: %s", text)
	}
	if _, err := updateHeartbeat(state, "landing-la", nil, nil, obj{"public_ip": "8.8.8.8"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("location lookup should be cached, calls=%d", calls)
	}
}

func TestCompleteHubSyncTaskStoresTopologyAndPublicEntries(t *testing.T) {
	root := t.TempDir()
	hubState := filepath.Join(root, "hub")
	reg := defaultHubRegistry()
	reg["agents"] = obj{
		"transit-la": obj{
			"kind":      agentRegistrationKind,
			"version":   version,
			"id":        "transit-la",
			"role":      "transit",
			"name":      "LA Transit",
			"transport": "poll",
			"topology":  obj{},
			"network":   obj{"reported_at": 1},
		},
	}
	if err := saveHubRegistry(hubState, reg); err != nil {
		t.Fatal(err)
	}
	task, err := createHubTask(hubState, asObj(asObj(reg["agents"])["transit-la"]), "sync_agent", nil, "batch-sync", "test-sync")
	if err != nil {
		t.Fatal(err)
	}
	result := obj{
		"success": true,
		"command": "sync_agent",
		"topology": obj{
			"links": []any{},
			"inbounds": []any{
				obj{"tag": "vless-in", "type": "vless", "users": []any{obj{"name": "alice", "uuid": "u"}}},
			},
			"reality_client": obj{"server_name": "addons.mozilla.org", "public_key": "pub", "short_id": "abcd"},
		},
		"network": obj{
			"public_entries": []any{obj{"use": "reality", "name": "main", "host": "edge.example", "public_port": 443}},
		},
	}
	if _, err := completeHubTask(hubState, str(task["id"]), "transit-la", result); err != nil {
		t.Fatal(err)
	}
	agents, err := listHubAgents(hubState)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := findHubAgentByID(agents, "transit-la")
	if err != nil {
		t.Fatal(err)
	}
	topo := asObj(agent["topology"])
	if len(asList(topo["inbounds"])) != 1 || asObj(topo["reality_client"])["public_key"] != "pub" {
		t.Fatalf("stored topology = %#v", topo)
	}
	network := asObj(agent["network"])
	entries := asList(network["public_entries"])
	if len(entries) != 1 || asObj(entries[0])["host"] != "edge.example" {
		t.Fatalf("stored network = %#v", network)
	}
}

func TestEnsureTransitRealityCreatesAndPreservesInbound(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "conf")
	state := filepath.Join(root, "state")
	summary, err := ensureTransitReality(conf, state, "::", 443, "vless-in", "www.cloudflare.com", "www.cloudflare.com", 443, "", "0123456789abcdef", "1m", true)
	if err != nil {
		t.Fatal(err)
	}
	if str(summary["public_key"]) == "" || str(summary["short_id"]) != "0123456789abcdef" || summary["updated"] != false {
		t.Fatalf("summary = %#v", summary)
	}
	path := filepath.Join(conf, "00-relaypilot-reality.json")
	data, err := loadJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	inbound := asObj(asList(data["inbounds"])[0])
	if str(inbound["type"]) != "vless" || str(inbound["tag"]) != "vless-in" || int64Value(inbound["listen_port"]) != 443 {
		t.Fatalf("inbound = %#v", inbound)
	}
	reality := asObj(asObj(inbound["tls"])["reality"])
	privateKey := str(reality["private_key"])
	if privateKey == "" {
		t.Fatalf("missing private key: %#v", inbound)
	}

	updated, err := ensureTransitReality(conf, state, "::", 8443, "vless-in", "www.cloudflare.com", "www.cloudflare.com", 443, "", "", "1m", true)
	if err != nil {
		t.Fatal(err)
	}
	if updated["updated"] != true || str(updated["short_id"]) != "0123456789abcdef" {
		t.Fatalf("updated = %#v", updated)
	}
	data, err = loadJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	inbound = asObj(asList(data["inbounds"])[0])
	if int64Value(inbound["listen_port"]) != 8443 || str(asObj(asObj(inbound["tls"])["reality"])["private_key"]) != privateKey {
		t.Fatalf("updated inbound = %#v", inbound)
	}
	clientInfo, err := vlessInboundClientInfo(conf, "vless-in")
	if err != nil {
		t.Fatal(err)
	}
	if str(clientInfo["public_key"]) != str(summary["public_key"]) || str(clientInfo["short_id"]) != "0123456789abcdef" {
		t.Fatalf("client info = %#v", clientInfo)
	}
}
