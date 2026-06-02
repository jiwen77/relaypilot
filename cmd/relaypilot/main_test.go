package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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

func TestHubLinkTransitLandingQueuesBindEndpointTask(t *testing.T) {
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

func TestHubLinkTransitLandingMeshModeWritesOverlayEndpoint(t *testing.T) {
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

	enrolled, err := agentEnrollInvite(invite, agentEnrollOptions{StateDir: agentState}, 10*time.Second)
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

	cfg, err := loadAgentEnrollment(agentState)
	if err != nil {
		t.Fatal(err)
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
	if !strings.Contains(status, "异常：1 离线") || !strings.Contains(status, "查看链路：/topology") {
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
	reg := defaultHubRegistry()
	reg["agents"] = obj{"transit-hk": obj{"kind": agentRegistrationKind, "version": version, "id": "transit-hk", "role": "transit", "name": "HK Transit", "transport": "poll"}}
	if err := saveHubRegistry(state, reg); err != nil {
		t.Fatal(err)
	}
	reply := handleTelegramHubUpdate(state, obj{"update_id": 1, "message": obj{"text": "/status"}})
	if !strings.Contains(reply, "Hub 管理面正常") {
		t.Fatalf("reply = %s", reply)
	}
	prefixedReply := handleTelegramHubUpdate(state, obj{"update_id": 2, "message": obj{"text": "/relaypilot_status"}})
	if !strings.Contains(prefixedReply, "Hub 管理面正常") {
		t.Fatalf("prefixed reply = %s", prefixedReply)
	}
	panelReply := handleTelegramHubReply(state, obj{"update_id": 4, "message": obj{"text": "/start", "chat": obj{"id": "999"}}})
	if !strings.Contains(panelReply.Text, "RelayPilot 控制中枢") || panelReply.ParseMode != "HTML" || len(panelReply.ReplyMarkup) == 0 {
		t.Fatalf("panel reply = %#v", panelReply)
	}
	updateReply := handleTelegramHubReply(state, obj{"update_id": 5, "callback_query": obj{"data": "rp:update", "message": obj{"chat": obj{"id": "999"}}}})
	if !strings.Contains(updateReply.Text, "<code>/relaypilot_update hub ") || !strings.Contains(updateReply.Text, "--restart</code>") {
		t.Fatalf("update reply = %#v", updateReply)
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
	if len(tasks) != 1 || tasks[0]["agent_id"] != "transit-hk" {
		t.Fatalf("tasks = %#v", tasks)
	}
	task := tasks[0]
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
	if !strings.Contains(cmdText, "/relaypilot_topology") || !strings.Contains(cmdText, "/relaypilot_results") {
		t.Fatalf("hub commands = %s", cmdText)
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
	if dryRegister["method"] != "setMyCommands" || strings.Contains(str(dryRegister["url"]), "SECRET_TOKEN") || !strings.Contains(str(payload["commands"]), "topology") {
		t.Fatalf("dry register = %#v", dryRegister)
	}
	drySend, err := sendTelegramMessageCLI(state, "hello", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if asObj(drySend["payload"])["text"] != "hello" {
		t.Fatalf("dry send = %#v", drySend)
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
		if str(asObj(raw)["tag"]) == "landing-hk-ss" {
			seenOutbound++
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
