package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func telegramDispatch(text, stateDir, conf string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return "", errors.New("telegram command must start with /")
	}
	command := normalizeTelegramCommand(parts[0])
	args := parts[1:]
	switch command {
	case "doctor":
		return fmt.Sprintf("doctor:\nstate_dir=%s\nconf=%s", stateDir, conf), nil
	case "endpoints", "list_endpoints":
		items, err := listEndpoints(stateDir)
		if err != nil {
			return "", err
		}
		return formatEndpointsText(items), nil
	case "show_endpoint":
		if len(args) == 0 {
			return "", errors.New("usage: /show_endpoint <name>")
		}
		items, err := listEndpoints(stateDir)
		if err != nil {
			return "", err
		}
		for _, item := range items {
			if str(item["name"]) == args[0] {
				return formatEndpointsText([]obj{item}), nil
			}
		}
		return "", fmt.Errorf("endpoint not found: %s", args[0])
	case "inspect_conf":
		target := conf
		if len(args) > 0 {
			target = args[0]
		}
		sum, err := inspectConfig(target)
		if err != nil {
			return "", err
		}
		return formatConfigText(sum), nil
	case "status":
		items, err := listEndpoints(stateDir)
		if err != nil {
			return "", err
		}
		sum, err := inspectConfig(conf)
		if err != nil {
			return formatEndpointsText(items) + "\n\nconfig: " + err.Error(), nil
		}
		return formatEndpointsText(items) + "\n\n" + formatConfigText(sum), nil
	default:
		return "", fmt.Errorf("unsupported telegram command: /%s", command)
	}
}

func formatEndpointsText(items []obj) string {
	if len(items) == 0 {
		return "endpoints: none"
	}
	var b strings.Builder
	b.WriteString("endpoints:\n")
	for _, item := range items {
		fmt.Fprintf(&b, "- %s (%s) %s %s:%v\n", str(item["name"]), str(item["protocol"]), str(item["tag"]), str(item["server"]), item["server_port"])
	}
	return strings.TrimRight(b.String(), "\n")
}

func executeTask(task obj, stateDir, conf string) obj {
	command := str(task["command"])
	switch command {
	case "export_endpoint":
		return executeExportEndpointTask(task, stateDir)
	case "sync_agent":
		return executeSyncAgentTask(task, stateDir, conf)
	case "bind_endpoint":
		return executeBindEndpointTask(task, stateDir, conf)
	case "probe_link":
		return executeProbeLinkTask(task)
	case "decommission_agent":
		return executeDecommissionAgentTask(task, stateDir, conf)
	case "unbind_endpoint":
		return executeUnbindEndpointTask(task, stateDir, conf)
	case "teardown_mesh":
		return executeTeardownMeshTask(task)
	case "self_update":
		return executeSelfUpdateTask(task)
	}
	if command != "status" && command != "doctor" && command != "endpoints" && command != "show_endpoint" && command != "inspect_conf" {
		return obj{"success": false, "error": "unsupported agent task command: " + command}
	}
	text := "/" + command
	if args := joinAny(asList(task["args"])); args != "" {
		text += " " + args
	}
	out, err := telegramDispatch(text, stateDir, conf)
	if err != nil {
		return obj{"success": false, "command": command, "error": err.Error()}
	}
	return obj{"success": true, "command": command, "text": out}
}

func executeSyncAgentTask(task obj, stateDir, conf string) obj {
	role := str(task["agent_role"])
	topology := collectTopology(role, stateDir, conf)
	network := obj{}
	if entries, err := listPublicEntries(stateDir); err == nil && len(entries) > 0 {
		network["public_entries"] = entries
	}
	return obj{
		"success":  true,
		"command":  "sync_agent",
		"agent_id": task["agent_id"],
		"topology": topology,
		"network":  network,
		"text":     "agent details synced",
	}
}

func executeExportEndpointTask(task obj, stateDir string) obj {
	payload := asObj(task["payload"])
	args := asList(task["args"])
	endpointName := ""
	if len(args) > 0 {
		endpointName = str(args[0])
	}
	if endpointName == "" {
		endpointName = str(payload["endpoint_name"])
	}
	if endpointName == "" {
		return obj{"success": false, "command": "export_endpoint", "error": "endpoint name is required"}
	}
	endpoint, err := exportEndpoint(stateDir, endpointName)
	if err != nil {
		return obj{"success": false, "command": "export_endpoint", "error": err.Error()}
	}
	meshSummary := obj{}
	if meshLanding := asObj(payload["mesh_landing"]); len(meshLanding) > 0 {
		summary, err := ensureWireGuardMesh(meshLanding)
		if err != nil {
			return obj{"success": false, "command": "export_endpoint", "error": err.Error()}
		}
		meshSummary = summary
	}
	result := obj{
		"success":       true,
		"command":       "export_endpoint",
		"endpoint_name": endpoint["name"],
		"endpoint_tag":  endpoint["tag"],
		"endpoint":      endpoint,
		"text":          fmt.Sprintf("endpoint exported: %s", endpoint["name"]),
	}
	if publicEntries, err := exportEndpointPublicEntries(stateDir, endpointName); err != nil {
		return obj{"success": false, "command": "export_endpoint", "error": err.Error()}
	} else if len(publicEntries) > 0 {
		result["public_entries"] = publicEntries
	}
	if len(meshSummary) > 0 {
		result["mesh"] = meshSummary
		result["text"] = fmt.Sprintf("mesh ready and endpoint exported: %s", endpoint["name"])
	}
	return result
}

func compactProbeError(err error) string {
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "unknown probe error"
	}
	if len(msg) > 240 {
		msg = msg[:240] + "..."
	}
	return msg
}

func executeProbeLinkTask(task obj) obj {
	payload := asObj(task["payload"])
	host := strings.TrimSpace(str(payload["host"]))
	port := int(int64Value(payload["port"]))
	timeoutSeconds := linkProbeTimeoutSeconds(int64Value(payload["timeout_seconds"]))
	result := obj{
		"command":         "probe_link",
		"host":            host,
		"port":            port,
		"timeout_seconds": timeoutSeconds,
		"landing_id":      payload["landing_id"],
		"endpoint_name":   payload["endpoint_name"],
		"target_source":   payload["target_source"],
	}
	if host == "" {
		result["success"] = false
		result["reachable"] = false
		result["error"] = "probe host is required"
		result["text"] = "链路检测不可达：缺少目标 host"
		return result
	}
	if port <= 0 || port > 65535 {
		result["success"] = false
		result["reachable"] = false
		result["error"] = fmt.Sprintf("invalid probe port: %d", port)
		result["text"] = "链路检测不可达：目标端口无效"
		return result
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	result["target"] = target
	start := time.Now()
	dialer := net.Dialer{Timeout: time.Duration(timeoutSeconds) * time.Second}
	conn, err := dialer.Dial("tcp", target)
	duration := time.Since(start)
	result["duration_ms"] = duration.Milliseconds()
	if err != nil {
		msg := compactProbeError(err)
		result["success"] = false
		result["reachable"] = false
		result["error"] = msg
		result["text"] = fmt.Sprintf("链路检测不可达：%s（%dms，超时 %ds）\n错误：%s", target, duration.Milliseconds(), timeoutSeconds, msg)
		return result
	}
	_ = conn.Close()
	result["success"] = true
	result["reachable"] = true
	result["text"] = fmt.Sprintf("链路检测连通：%s（%dms，超时 %ds）", target, duration.Milliseconds(), timeoutSeconds)
	return result
}

func remoteDecommissionAllowed(stateDir string) bool {
	if truthy(os.Getenv("RELAYPILOT_ALLOW_REMOTE_DECOMMISSION")) {
		return true
	}
	policy, err := loadJSON(agentPolicyPath(stateDir))
	if err != nil {
		return false
	}
	return truthy(policy["allow_remote_decommission"])
}

func decommissionDelaySeconds(v int64) int {
	if v <= 0 {
		return 12
	}
	if v < 5 {
		return 5
	}
	if v > 300 {
		return 300
	}
	return int(v)
}

func relayPilotCLIArgsForDecommission(mode string) ([]string, error) {
	switch mode {
	case "detach":
		return []string{"leave-hub"}, nil
	case "purge-managed-proxy":
		return []string{"reset-agent"}, nil
	case "uninstall":
		return []string{"uninstall", "--full", "--purge-proxy-config", "--yes"}, nil
	default:
		return nil, fmt.Errorf("unsupported decommission mode: %s", mode)
	}
}

func managedProxyFragmentPaths(conf string) []string {
	var paths []string
	if conf != "" {
		if matches, err := filepath.Glob(filepath.Join(conf, "*relaypilot*.json")); err == nil {
			paths = append(paths, matches...)
		}
	}
	wgDir := firstNonEmpty(strings.TrimSpace(os.Getenv("MESH_CONFIG_DIR")), defaultMeshConfigDir)
	if entries, err := os.ReadDir(wgDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
				continue
			}
			path := filepath.Join(wgDir, entry.Name())
			data, err := os.ReadFile(path)
			if err == nil && bytes.Contains(data, []byte(wireGuardConfigMarker)) {
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func decommissionPlanLines(mode, stateDir, conf string, cliArgs []string) []string {
	lines := []string{
		"模式：" + decommissionModeLabel(mode),
		"将执行：" + "relaypilot " + strings.Join(cliArgs, " "),
	}
	switch mode {
	case "detach":
		lines = append(lines,
			"将停止并移除 Agent 轮询服务。",
			"将删除 Hub 接入凭证。",
			"保留 RelayPilot 程序与代理配置。",
		)
	case "purge-managed-proxy":
		lines = append(lines,
			"将删除 Hub 接入凭证、endpoint 缓存与公网入口记录。",
			"将删除 RelayPilot 托管的 sing-box 片段和 WireGuard mesh。",
			"保留 RelayPilot 程序。",
		)
	case "uninstall":
		lines = append(lines,
			"将卸载 RelayPilot 程序、服务和状态目录。",
			"将删除 RelayPilot 托管的 sing-box 片段和 WireGuard mesh。",
		)
	}
	lines = append(lines,
		"状态目录："+stateDir,
		"代理配置目录："+conf,
	)
	fragments := managedProxyFragmentPaths(conf)
	if len(fragments) == 0 {
		lines = append(lines, "当前未发现 RelayPilot 托管代理片段。")
	} else {
		lines = append(lines, "将清理托管代理片段：")
		for _, path := range fragments {
			lines = append(lines, "- "+path)
		}
	}
	lines = append(lines, "不会执行任意 shell；不会删除未带 RelayPilot 标记的用户配置。")
	return lines
}

func decommissionCommandEnv(stateDir, conf string) []string {
	env := os.Environ()
	env = append(env,
		"STATE_DIR="+stateDir,
		"CONF_DIR="+conf,
		"RELAYPILOT_NONINTERACTIVE=1",
		"RELAYPILOT_REMOTE_DECOMMISSION=1",
	)
	if meshDir := strings.TrimSpace(os.Getenv("MESH_CONFIG_DIR")); meshDir != "" {
		env = append(env, "MESH_CONFIG_DIR="+meshDir)
	}
	return env
}

func scheduleDecommissionCleanup(mode, stateDir, conf string, delaySeconds int) (obj, error) {
	cli, err := relayPilotCLIPath()
	if err != nil {
		return nil, err
	}
	args, err := relayPilotCLIArgsForDecommission(mode)
	if err != nil {
		return nil, err
	}
	env := decommissionCommandEnv(stateDir, conf)
	if strings.EqualFold(strings.TrimSpace(os.Getenv("RELAYPILOT_DECOMMISSION_EXEC_MODE")), "foreground") {
		cmd := exec.Command(cli, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		summary := obj{"scheduler": "foreground", "cli": cli, "args": stringsToAny(args), "output": lastNonEmptyLines(string(out), 8)}
		if err != nil {
			return summary, fmt.Errorf("decommission cleanup failed: %w: %s", err, lastNonEmptyLines(string(out), 8))
		}
		return summary, nil
	}
	if systemdRun, err := exec.LookPath("systemd-run"); err == nil && os.Geteuid() == 0 && strings.TrimSpace(os.Getenv("RELAYPILOT_DECOMMISSION_SCHEDULER")) != "sh" {
		unit := "relaypilot-decommission-" + mustRandomHex(4)
		runArgs := []string{
			"--unit", unit,
			"--collect",
			"--property=Type=oneshot",
			"--setenv=STATE_DIR=" + stateDir,
			"--setenv=CONF_DIR=" + conf,
			"--setenv=RELAYPILOT_NONINTERACTIVE=1",
			"--setenv=RELAYPILOT_REMOTE_DECOMMISSION=1",
			"sh", "-c", `sleep "$1"; shift; exec "$@"`,
			"relaypilot-decommission", strconv.Itoa(delaySeconds), cli,
		}
		runArgs = append(runArgs, args...)
		out, err := exec.Command(systemdRun, runArgs...).CombinedOutput()
		summary := obj{"scheduler": "systemd-run", "unit": unit, "cli": cli, "args": stringsToAny(args), "output": lastNonEmptyLines(string(out), 8)}
		if err != nil {
			return summary, fmt.Errorf("systemd-run decommission schedule failed: %w: %s", err, lastNonEmptyLines(string(out), 8))
		}
		return summary, nil
	}
	shellArgs := []string{"-c", `sleep "$1"; shift; exec "$@"`, "relaypilot-decommission", strconv.Itoa(delaySeconds), cli}
	shellArgs = append(shellArgs, args...)
	cmd := exec.Command("sh", shellArgs...)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return obj{"scheduler": "sh", "pid": cmd.Process.Pid, "cli": cli, "args": stringsToAny(args)}, nil
}

func executeDecommissionAgentTask(task obj, stateDir, conf string) obj {
	stateDir = firstNonEmpty(strings.TrimSpace(stateDir), defaultStateDir)
	conf = firstNonEmpty(strings.TrimSpace(conf), defaultConfDir)
	payload := asObj(task["payload"])
	mode, err := normalizeDecommissionMode(firstNonEmpty(str(payload["mode"]), joinAny(asList(task["args"])), "uninstall"))
	if err != nil {
		return obj{"success": false, "command": "decommission_agent", "error": err.Error()}
	}
	if !remoteDecommissionAllowed(stateDir) {
		return obj{"success": false, "command": "decommission_agent", "mode": mode, "error": "remote decommission is disabled on this agent; set allow_remote_decommission=true locally"}
	}
	cliArgs, err := relayPilotCLIArgsForDecommission(mode)
	if err != nil {
		return obj{"success": false, "command": "decommission_agent", "mode": mode, "error": err.Error()}
	}
	dryRun := truthy(payload["dry_run"])
	delaySeconds := decommissionDelaySeconds(int64Value(payload["delay_seconds"]))
	lines := append([]string{"远程退役预览"}, decommissionPlanLines(mode, stateDir, conf, cliArgs)...)
	result := obj{
		"success":       true,
		"command":       "decommission_agent",
		"mode":          mode,
		"dry_run":       dryRun,
		"delay_seconds": delaySeconds,
	}
	if dryRun {
		result["text"] = strings.Join(lines, "\n")
		return result
	}
	schedule, err := scheduleDecommissionCleanup(mode, stateDir, conf, delaySeconds)
	if err != nil {
		return obj{"success": false, "command": "decommission_agent", "mode": mode, "dry_run": false, "error": err.Error(), "schedule": schedule}
	}
	result["schedule"] = schedule
	result["text"] = fmt.Sprintf("远程退役已确认：%s；已安排约 %d 秒后执行本机清理。", decommissionModeLabel(mode), delaySeconds)
	return result
}

func executeBindEndpointTask(task obj, stateDir, conf string) obj {
	payload := asObj(task["payload"])
	endpoint := asObj(payload["endpoint"])
	if len(endpoint) == 0 {
		return obj{"success": false, "command": "bind_endpoint", "error": "endpoint payload is required"}
	}
	endpoint, err := validateEndpoint(endpoint, true)
	if err != nil {
		return obj{"success": false, "command": "bind_endpoint", "error": err.Error()}
	}
	meshSummary := obj{}
	if meshTransit := asObj(payload["mesh_transit"]); len(meshTransit) > 0 {
		summary, err := ensureWireGuardMesh(meshTransit)
		if err != nil {
			return obj{"success": false, "command": "bind_endpoint", "error": err.Error()}
		}
		meshSummary = summary
	}
	authUser := firstNonEmpty(str(payload["auth_user"]), str(endpoint["name"]))
	if authUser == "" {
		return obj{"success": false, "command": "bind_endpoint", "error": "auth_user is required"}
	}
	endpointPath, err := endpointStorePath(stateDir, str(endpoint["name"]))
	if err != nil {
		return obj{"success": false, "command": "bind_endpoint", "error": err.Error()}
	}
	if err := writeJSON(endpointPath, endpoint, 0o600); err != nil {
		return obj{"success": false, "command": "bind_endpoint", "error": err.Error()}
	}
	summary, err := bindTransit(conf, endpoint, str(payload["inbound_tag"]), authUser, str(payload["client_uuid"]), firstNonEmpty(str(payload["flow"]), "xtls-rprx-vision"), true, stateDir, true)
	if err != nil {
		return obj{"success": false, "command": "bind_endpoint", "error": err.Error()}
	}
	apply, applyErr := applyDataPlaneChange(conf, payload)
	if applyErr != nil {
		return obj{
			"success":       false,
			"command":       "bind_endpoint",
			"endpoint_name": endpoint["name"],
			"endpoint_tag":  endpoint["tag"],
			"auth_user":     authUser,
			"summary":       summary,
			"apply":         apply,
			"error":         applyErr.Error(),
			"text":          fmt.Sprintf("linked auth_user %s to endpoint %s, but data-plane apply failed: %s", authUser, endpoint["name"], applyErr.Error()),
		}
	}
	applyText := formatDataPlaneApplyText(apply)
	text := fmt.Sprintf("linked auth_user %s to endpoint %s", authUser, endpoint["name"])
	if applyText != "" {
		text += "；" + applyText
	}
	result := obj{
		"success":       true,
		"command":       "bind_endpoint",
		"endpoint_name": endpoint["name"],
		"endpoint_tag":  endpoint["tag"],
		"link_mode":     firstNonEmpty(str(payload["link_mode"]), str(endpoint["link_mode"]), "direct"),
		"auth_user":     authUser,
		"summary":       summary,
		"apply":         apply,
		"text":          text,
	}
	if len(meshSummary) > 0 {
		result["mesh"] = meshSummary
		result["text"] = fmt.Sprintf("mesh ready and linked auth_user %s to endpoint %s", authUser, endpoint["name"])
		if applyText != "" {
			result["text"] = str(result["text"]) + "；" + applyText
		}
	}
	return result
}

func executeUnbindEndpointTask(task obj, stateDir, conf string) obj {
	payload := asObj(task["payload"])
	summary, err := unbindTransit(conf, stateDir, str(payload["inbound_tag"]), str(payload["auth_user"]), str(payload["endpoint_name"]), str(payload["outbound_tag"]), truthy(payload["teardown_mesh"]), asObj(payload["mesh"]), true)
	if err != nil {
		return obj{"success": false, "command": "unbind_endpoint", "error": err.Error()}
	}
	return obj{"success": true, "command": "unbind_endpoint", "summary": summary, "text": fmt.Sprintf("unbound endpoint %s", firstNonEmpty(str(payload["endpoint_name"]), str(payload["outbound_tag"])))}
}

func executeTeardownMeshTask(task obj) obj {
	payload := asObj(task["payload"])
	summary, err := teardownWireGuardMesh(firstNonEmpty(str(payload["config_dir"]), defaultMeshConfigDir), str(payload["interface"]))
	if err != nil {
		return obj{"success": false, "command": "teardown_mesh", "error": err.Error()}
	}
	return obj{"success": true, "command": "teardown_mesh", "summary": summary, "text": fmt.Sprintf("mesh removed: %s", summary["interface"])}
}

func executeSelfUpdateTask(task obj) obj {
	payload := asObj(task["payload"])
	updateVersion := firstNonEmpty(str(payload["version"]), joinAny(asList(task["args"])))
	updateVersion = strings.TrimSpace(updateVersion)
	if updateVersion == "" {
		return obj{"success": false, "command": "self_update", "error": "update version is required"}
	}
	updateVersion = strings.Fields(updateVersion)[0]
	out, err := runRelayPilotUpdate(updateVersion)
	if err != nil {
		return obj{"success": false, "command": "self_update", "version": updateVersion, "error": err.Error(), "text": lastNonEmptyLines(out, 8)}
	}
	text := selfUpdateResultText(updateVersion, out)
	if truthy(payload["restart_services"]) {
		serviceName := firstNonEmpty(str(payload["service_name"]), envOrDefault("RELAYPILOT_AGENT_SERVICE_NAME", "relaypilot-agent"))
		if relayPilotUpdateAlreadyCurrent(out) {
			text += "\n无需更新，未安排重启。"
		} else if err := scheduleServiceRestart([]string{serviceName}, agentServiceRestartDelaySecs); err != nil {
			text += "\nAgent 服务重启计划失败：" + err.Error()
		} else {
			text += fmt.Sprintf("\n已安排约 %d 秒后尝试重启 Agent 服务。", agentServiceRestartDelaySecs)
		}
	}
	return obj{"success": true, "command": "self_update", "version": updateVersion, "restart_services": truthy(payload["restart_services"]), "text": text}
}

func selfUpdateResultText(updateVersion, output string) string {
	resolvedVersion := relayPilotVersionFromUpdateOutput(updateVersion, output)
	if relayPilotUpdateAlreadyCurrent(output) {
		return strings.Join([]string{
			"RelayPilot 已是最新版本",
			"当前版本：" + resolvedVersion,
			"无需更新。",
		}, "\n")
	}
	lines := []string{"RelayPilot 已更新"}
	if resolvedVersion != "" && resolvedVersion != updateVersion {
		lines = append(lines, "当前版本："+resolvedVersion)
	} else {
		lines = append(lines, "版本："+updateVersion)
	}
	return strings.Join(lines, "\n")
}

func readAgentToken(token, tokenFile string) (string, error) {
	if token != "" {
		return strings.TrimSpace(token), nil
	}
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		return strings.TrimSpace(string(data)), err
	}
	if env := os.Getenv("AGENT_TOKEN"); env != "" {
		return strings.TrimSpace(env), nil
	}
	return "", errors.New("agent token is required")
}

func httpJSONRequest(method, rawURL string, payload any, token, agentID string, timeout time.Duration) (obj, error) {
	return httpJSONRequestWithTLS(method, rawURL, payload, token, agentID, timeout, nil)
}

func httpJSONRequestWithTLS(method, rawURL string, payload any, token, agentID string, timeout time.Duration, tlsConfig *tls.Config) (obj, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		if len(body) > maxHTTPBodyBytes {
			return nil, fmt.Errorf("request payload too large: %d bytes > %d", len(body), maxHTTPBodyBytes)
		}
	}
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		u, _ := url.Parse(rawURL)
		pathQ := u.EscapedPath()
		if pathQ == "" {
			pathQ = "/"
		}
		if u.RawQuery != "" {
			pathQ += "?" + u.RawQuery
		}
		ts := strconv.FormatInt(now(), 10)
		nonce := mustRandomHex(16)
		sig, err := signAgentRequest(token, method, pathQ, body, ts, nonce, agentID)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Agent-Id", agentID)
		req.Header.Set("X-Agent-Timestamp", ts)
		req.Header.Set("X-Agent-Nonce", nonce)
		req.Header.Set("X-Agent-Signature", sig)
	}
	client := &http.Client{Timeout: timeout}
	if tlsConfig != nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = tlsConfig
		client.Transport = transport
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body, maxHTTPBodyBytes)
	if err != nil {
		return nil, err
	}
	var out obj
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("hub api %s %s failed (%d): %s", method, rawURL, resp.StatusCode, str(out["error"]))
	}
	return out, nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("body too large: %d bytes > %d", len(data), limit)
	}
	return data, nil
}

type agentPoller struct {
	hubURL           string
	agentID          string
	token            string
	role             string
	stateDir         string
	conf             string
	ipMode           string
	maxTasks         int
	timeout          time.Duration
	topologyInterval time.Duration
	publicIPInterval time.Duration
	publicIPProbe    func(time.Duration) (string, error)
	cachedTopology   obj
	cachedHealth     obj
	topologyExpires  time.Time
	cachedPublicIP   string
	publicIPChecked  int64
	publicIPExpires  time.Time
	publicIPError    string
	tlsConfig        *tls.Config
}

func newAgentPoller(hubURL, agentID, token, role, stateDir, conf string, maxTasks int, timeout time.Duration, topologyIntervalSeconds int) *agentPoller {
	if maxTasks < 0 {
		maxTasks = 0
	}
	if maxTasks > maxHubTasksPerPoll {
		maxTasks = maxHubTasksPerPoll
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if topologyIntervalSeconds < 0 {
		topologyIntervalSeconds = 0
	}
	return &agentPoller{
		hubURL:           hubURL,
		agentID:          agentID,
		token:            token,
		role:             role,
		stateDir:         stateDir,
		conf:             conf,
		ipMode:           "static",
		maxTasks:         maxTasks,
		timeout:          timeout,
		topologyInterval: time.Duration(topologyIntervalSeconds) * time.Second,
		publicIPInterval: time.Duration(defaultPublicIPIntervalSeconds) * time.Second,
		publicIPProbe:    detectPublicIPWithTimeout,
	}
}

func normalizeIPMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "static", nil
	}
	switch mode {
	case "static", "dynamic":
		return mode, nil
	default:
		return "", fmt.Errorf("ip-mode must be static or dynamic: %q", mode)
	}
}

func configurePollerNetwork(p *agentPoller, ipMode string, publicIPIntervalSeconds int) error {
	mode, err := normalizeIPMode(ipMode)
	if err != nil {
		return err
	}
	p.ipMode = mode
	if publicIPIntervalSeconds <= 0 {
		publicIPIntervalSeconds = defaultPublicIPIntervalSeconds
	}
	p.publicIPInterval = time.Duration(publicIPIntervalSeconds) * time.Second
	return nil
}

type fileStamp struct {
	ModUnixNano int64
	Size        int64
}

func statFileStamp(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{ModUnixNano: info.ModTime().UnixNano(), Size: info.Size()}, nil
}

func newAgentPollerFromEnrollmentFile(path, stateDir, conf string, maxTasks int, timeout time.Duration, topologyIntervalSeconds int) (*agentPoller, fileStamp, error) {
	cfg, err := loadAgentEnrollmentFile(path)
	if err != nil {
		return nil, fileStamp{}, err
	}
	if cfg.Role == "" {
		cfg.Role = "transit"
	}
	secret, err := readAgentToken("", cfg.TokenFile)
	if err != nil {
		return nil, fileStamp{}, err
	}
	tlsConfig, err := loadAgentTLSConfig(cfg.CACertPath, cfg.ClientCertPath, cfg.ClientKeyPath, "")
	if err != nil {
		return nil, fileStamp{}, err
	}
	poller := newAgentPoller(cfg.HubURL, cfg.AgentID, secret, cfg.Role, stateDir, conf, maxTasks, timeout, topologyIntervalSeconds)
	poller.tlsConfig = tlsConfig
	if err := configurePollerNetwork(poller, cfg.IPMode, cfg.PublicIPIntervalSeconds); err != nil {
		return nil, fileStamp{}, err
	}
	stamp, err := statFileStamp(path)
	if err != nil {
		return nil, fileStamp{}, err
	}
	return poller, stamp, nil
}

func reloadAgentPollerFromEnrollmentFileIfChanged(path string, previous fileStamp, current *agentPoller, stateDir, conf string, maxTasks int, timeout time.Duration, topologyIntervalSeconds int) (*agentPoller, fileStamp, bool, error) {
	next, err := statFileStamp(path)
	if err != nil {
		return current, previous, false, err
	}
	if next == previous {
		return current, previous, false, nil
	}
	poller, stamp, err := newAgentPollerFromEnrollmentFile(path, stateDir, conf, maxTasks, timeout, topologyIntervalSeconds)
	if err != nil {
		return current, previous, false, err
	}
	return poller, stamp, true, nil
}

func (p *agentPoller) topology() (obj, obj) {
	if p.topologyInterval > 0 && p.cachedTopology != nil && time.Now().Before(p.topologyExpires) {
		return p.cachedTopology, p.cachedHealth
	}
	topo := collectTopology(p.role, p.stateDir, p.conf)
	health := healthFromTopology(topo)
	if p.topologyInterval > 0 {
		p.cachedTopology = topo
		p.cachedHealth = health
		p.topologyExpires = time.Now().Add(p.topologyInterval)
	}
	return topo, health
}

func (p *agentPoller) publicIP() (string, int64, string) {
	if p.publicIPInterval <= 0 {
		p.publicIPInterval = time.Duration(defaultPublicIPIntervalSeconds) * time.Second
	}
	if time.Now().Before(p.publicIPExpires) {
		return p.cachedPublicIP, p.publicIPChecked, p.publicIPError
	}
	probe := p.publicIPProbe
	if probe == nil {
		probe = detectPublicIPWithTimeout
	}
	probeTimeout := defaultPublicIPProbeTimeout
	if p.timeout > 0 && p.timeout < probeTimeout {
		probeTimeout = p.timeout
	}
	ip, err := probe(probeTimeout)
	p.publicIPChecked = now()
	p.publicIPExpires = time.Now().Add(p.publicIPInterval)
	if err != nil {
		p.publicIPError = err.Error()
		return p.cachedPublicIP, p.publicIPChecked, p.publicIPError
	}
	if parsed := net.ParseIP(strings.TrimSpace(ip)); parsed == nil {
		p.publicIPError = "public IP probe returned non-IP: " + strings.TrimSpace(ip)
		return p.cachedPublicIP, p.publicIPChecked, p.publicIPError
	}
	p.cachedPublicIP = strings.TrimSpace(ip)
	p.publicIPError = ""
	return p.cachedPublicIP, p.publicIPChecked, ""
}

func (p *agentPoller) network() obj {
	out := obj{"reported_at": now()}
	mode, err := normalizeIPMode(p.ipMode)
	if err != nil {
		mode = "static"
	}
	if mode != "dynamic" {
		return out
	}
	out["ip_mode"] = "dynamic"
	ip, checkedAt, errText := p.publicIP()
	if checkedAt > 0 {
		out["public_ip_checked_at"] = checkedAt
	}
	if ip != "" {
		out["public_ip"] = ip
	}
	if errText != "" {
		out["public_ip_error"] = errText
	}
	return out
}

func (p *agentPoller) pollOnce() (obj, error) {
	if p.hubURL == "" || p.agentID == "" {
		return nil, errors.New("hub-url and agent-id are required")
	}
	_, health := p.topology()
	base := strings.TrimRight(p.hubURL, "/")
	heartbeat, err := httpJSONRequestWithTLS("POST", base+"/api/agents/"+url.PathEscape(p.agentID)+"/heartbeat", obj{"health": health, "network": p.network()}, p.token, p.agentID, p.timeout, p.tlsConfig)
	if err != nil {
		return nil, err
	}
	tasksResp, err := httpJSONRequestWithTLS("GET", base+"/api/agents/"+url.PathEscape(p.agentID)+"/tasks?limit="+strconv.Itoa(p.maxTasks), nil, p.token, p.agentID, p.timeout, p.tlsConfig)
	if err != nil {
		return nil, err
	}
	var completed []any
	for _, raw := range asList(tasksResp["tasks"]) {
		task := asObj(raw)
		res := executeTask(task, p.stateDir, p.conf)
		done, err := httpJSONRequestWithTLS("POST", base+"/api/tasks/"+url.PathEscape(str(task["id"]))+"/result", obj{"agent_id": p.agentID, "result": res}, p.token, p.agentID, p.timeout, p.tlsConfig)
		if err != nil {
			return nil, err
		}
		completed = append(completed, done["task"])
	}
	return obj{"agent_id": p.agentID, "registered": false, "heartbeat": heartbeat["agent"], "leased": len(asList(tasksResp["tasks"])), "completed": completed}, nil
}

func agentPollOnce(hubURL, agentID, token, role, stateDir, conf string, maxTasks int, timeout time.Duration) (obj, error) {
	return newAgentPoller(hubURL, agentID, token, role, stateDir, conf, maxTasks, timeout, 0).pollOnce()
}

func agentPollOnceWithTLS(hubURL, agentID, token, role, stateDir, conf string, maxTasks int, timeout time.Duration, tlsConfig *tls.Config) (obj, error) {
	poller := newAgentPoller(hubURL, agentID, token, role, stateDir, conf, maxTasks, timeout, 0)
	poller.tlsConfig = tlsConfig
	return poller.pollOnce()
}

func agentPollLoop(hubURL, agentID, token, role, stateDir, conf string, interval, maxTasks int, timeout time.Duration, topologyIntervalSeconds int) error {
	return agentPollLoopWithTLS(hubURL, agentID, token, role, stateDir, conf, interval, maxTasks, timeout, topologyIntervalSeconds, nil)
}

func agentPollLoopWithTLS(hubURL, agentID, token, role, stateDir, conf string, interval, maxTasks int, timeout time.Duration, topologyIntervalSeconds int, tlsConfig *tls.Config) error {
	return agentPollLoopWithTLSAndNetwork(hubURL, agentID, token, role, stateDir, conf, interval, maxTasks, timeout, topologyIntervalSeconds, tlsConfig, "static", defaultPublicIPIntervalSeconds)
}

func agentPollLoopWithTLSAndNetwork(hubURL, agentID, token, role, stateDir, conf string, interval, maxTasks int, timeout time.Duration, topologyIntervalSeconds int, tlsConfig *tls.Config, ipMode string, publicIPIntervalSeconds int) error {
	if interval < 1 {
		interval = 1
	}
	poller := newAgentPoller(hubURL, agentID, token, role, stateDir, conf, maxTasks, timeout, topologyIntervalSeconds)
	poller.tlsConfig = tlsConfig
	if err := configurePollerNetwork(poller, ipMode, publicIPIntervalSeconds); err != nil {
		return err
	}
	backoff := time.Duration(interval) * time.Second
	maxBackoff := 5 * time.Minute
	for {
		result, err := poller.pollOnce()
		if err != nil {
			wire, _ := json.Marshal(obj{"ok": false, "agent_id": agentID, "error": err.Error(), "retry_in_seconds": int(backoff.Seconds())})
			fmt.Fprintln(os.Stderr, string(wire))
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = time.Duration(interval) * time.Second
		wire, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Println(string(wire))
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func agentPollLoopWithEnrollmentFile(enrollmentFile, stateDir, conf string, interval, maxTasks int, timeout time.Duration, topologyIntervalSeconds int) error {
	if interval < 1 {
		interval = 1
	}
	poller, stamp, err := newAgentPollerFromEnrollmentFile(enrollmentFile, stateDir, conf, maxTasks, timeout, topologyIntervalSeconds)
	if err != nil {
		return err
	}
	backoff := time.Duration(interval) * time.Second
	maxBackoff := 5 * time.Minute
	for {
		nextPoller, nextStamp, changed, err := reloadAgentPollerFromEnrollmentFileIfChanged(enrollmentFile, stamp, poller, stateDir, conf, maxTasks, timeout, topologyIntervalSeconds)
		if err != nil {
			wire, _ := json.Marshal(obj{"ok": false, "agent_id": poller.agentID, "error": "enrollment reload failed: " + err.Error()})
			fmt.Fprintln(os.Stderr, string(wire))
		} else if changed {
			poller, stamp = nextPoller, nextStamp
			backoff = time.Duration(interval) * time.Second
		}

		result, err := poller.pollOnce()
		if err != nil {
			wire, _ := json.Marshal(obj{"ok": false, "agent_id": poller.agentID, "error": err.Error(), "retry_in_seconds": int(backoff.Seconds())})
			fmt.Fprintln(os.Stderr, string(wire))
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = time.Duration(interval) * time.Second
		wire, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Println(string(wire))
		time.Sleep(time.Duration(interval) * time.Second)
	}
}
