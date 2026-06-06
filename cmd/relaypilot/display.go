package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func firstLandingEndpointName(landing obj) string {
	for _, raw := range asList(asObj(landing["topology"])["endpoints"]) {
		name := str(asObj(raw)["name"])
		if name != "" {
			return name
		}
	}
	return ""
}

func selectAgents(agents []obj, selector string) []obj {
	selector = strings.TrimSpace(selector)
	if selector == "" || selector == "hub" {
		return []obj{}
	}
	if selector == "all" {
		return agents
	}
	if selector == "transit" || selector == "landing" {
		var out []obj
		for _, agent := range agents {
			if selector == str(agent["role"]) {
				out = append(out, agent)
			}
		}
		return out
	}
	prefix, rest, hasPrefix := strings.Cut(selector, ":")
	if hasPrefix && prefix == "role" {
		var out []obj
		for _, agent := range agents {
			if rest == str(agent["role"]) {
				out = append(out, agent)
			}
		}
		return out
	}
	if hasPrefix && prefix == "label" {
		key, value, ok := strings.Cut(rest, "=")
		if !ok {
			return []obj{}
		}
		var out []obj
		for _, agent := range agents {
			if str(asObj(agent["labels"])[key]) == value {
				out = append(out, agent)
			}
		}
		return out
	}
	var out []obj
	for _, agent := range agents {
		if selector == str(agent["id"]) || selector == str(agent["name"]) {
			out = append(out, agent)
		}
	}
	return out
}

func splitPendingAgents(agents []obj) (ready, pending []obj) {
	for _, agent := range agents {
		if isPendingAgent(agent) {
			pending = append(pending, agent)
		} else {
			ready = append(ready, agent)
		}
	}
	return ready, pending
}

func roleIcon(role string) string {
	if role == "transit" {
		return "🛫"
	}
	if role == "landing" {
		return "🛬"
	}
	return "🧭"
}

func roleLabel(role string) string {
	switch role {
	case "transit":
		return "中转"
	case "landing":
		return "落地"
	case "hub":
		return "Hub"
	default:
		return firstNonEmpty(role, "未知")
	}
}

func agentNetworkLabel(agent obj) string {
	network := asObj(agent["network"])
	mode, _ := normalizeIPMode(str(network["ip_mode"]))
	if mode == "" {
		mode = "static"
	}
	join := func(parts ...string) string {
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
		return strings.Join(out, " · ")
	}
	location := agentLocationLabel(network, networkLocationIP(network))
	if mode == "dynamic" {
		if ip := str(network["public_ip"]); ip != "" {
			return join("动态", "IP "+maskIPForDisplay(ip), location)
		}
		if errText := str(network["public_ip_error"]); errText != "" {
			return join("动态", "公网IP探测失败", location)
		}
		return join("动态", location)
	}
	if ip := str(network["observed_ip"]); ip != "" {
		return join("静态", "IP "+maskIPForDisplay(ip), location)
	}
	if len(network) > 0 {
		return join("静态", location)
	}
	return ""
}

func agentLocationLabel(network obj, ip string) string {
	location := asObj(network["location"])
	label := strings.TrimSpace(str(location["label"]))
	if label == "" {
		return ""
	}
	if locIP := strings.TrimSpace(str(location["ip"])); locIP != "" && ip != "" && locIP != ip {
		return ""
	}
	return "位置 " + label
}

func formatAgentsText(agents []obj) string {
	if len(agents) == 0 {
		return "暂无已注册 agent。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📡 节点列表：%d\n", len(agents))
	for i, agent := range agents {
		if i > 0 {
			b.WriteString("\n")
		}
		name := str(agent["name"])
		if name == "" {
			name = "未命名"
		}
		fmt.Fprintf(&b, "%s %s %s · %s · %s\n", livenessIcon(agent), roleIcon(str(agent["role"])), str(agent["id"]), roleLabel(str(agent["role"])), name)
		parts := []string{}
		if state := agentLiveness(agent); state != "online" {
			parts = append(parts, livenessLabel(agent))
		}
		if labels := formatLabels(asObj(agent["labels"])); labels != "" {
			parts = append(parts, labels)
		}
		if networkLabel := agentNetworkLabel(agent); networkLabel != "" {
			parts = append(parts, networkLabel)
		}
		if syncLabel := agentSyncLabel(agent); syncLabel != "" {
			parts = append(parts, syncLabel)
		}
		if len(parts) == 0 {
			parts = append(parts, livenessLabel(agent))
		}
		fmt.Fprintf(&b, "   %s\n", strings.Join(parts, " · "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func agentSyncLabel(agent obj) string {
	syncAt := int64Value(agent["sync_at"])
	if syncAt <= 0 {
		return "未刷新详情"
	}
	age := now() - syncAt
	if age < 0 {
		age = 0
	}
	if age < 60 {
		return "上次刷新 刚刚"
	}
	if age < 3600 {
		return fmt.Sprintf("上次刷新 %d 分钟前", age/60)
	}
	if age < 86400 {
		return fmt.Sprintf("上次刷新 %d 小时前", age/3600)
	}
	return fmt.Sprintf("上次刷新 %d 天前", age/86400)
}

func isPendingAgent(agent obj) bool {
	return str(agent["enrollment_status"]) == "pending" && int64Value(agent["last_seen"]) == 0
}

func livenessIcon(agent obj) string {
	switch agentLiveness(agent) {
	case "pending":
		return "🕓"
	case "online":
		return "🟢"
	case "stale":
		return "🟡"
	default:
		return "🔴"
	}
}

func livenessLabel(agent obj) string {
	switch agentLiveness(agent) {
	case "pending":
		return "待接入"
	case "online":
		return "在线"
	case "stale":
		return "可能掉线"
	default:
		return "离线"
	}
}

func agentLiveness(agent obj) string {
	if isPendingAgent(agent) {
		return "pending"
	}
	age := now() - int64Value(firstNonNil(agent["last_seen"], agent["updated_at"]))
	if age <= heartbeatStaleSeconds {
		return "online"
	}
	if age <= heartbeatOfflineSeconds {
		return "stale"
	}
	return "offline"
}

func formatSecondsHuman(seconds int64) string {
	if seconds <= 0 {
		seconds = defaultEnrollCodeTTLSeconds
	}
	if seconds%86400 == 0 {
		return fmt.Sprintf("%d 天", seconds/86400)
	}
	if seconds%3600 == 0 {
		return fmt.Sprintf("%d 小时", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%d 分钟", seconds/60)
	}
	return fmt.Sprintf("%d 秒", seconds)
}

func formatUnixLocal(ts int64) string {
	if ts <= 0 {
		return "未知"
	}
	return time.Unix(ts, 0).Local().Format("2006-01-02 15:04:05 MST")
}

func formatHubEnrollInviteText(invite obj) string {
	lines := []string{
		"✅ Agent 邀请码已生成",
		fmt.Sprintf("节点：%s（%s）", str(invite["agent_id"]), roleLabel(str(invite["role"]))),
		"Hub：" + str(invite["hub_url"]),
		fmt.Sprintf("有效期：%s，单次使用（到 %s）", formatSecondsHuman(int64Value(invite["ttl_seconds"])), formatUnixLocal(int64Value(invite["expires_at"]))),
		"状态：已在 Hub 中创建为「待接入」",
		"",
		"安装命令（复制到目标 Agent 机器执行）：",
		str(invite["install_command"]),
	}
	return strings.Join(lines, "\n")
}

func hubStatusText(stateDir string, agents []obj) string {
	taskSummary, _ := cachedHubTaskSummary(stateDir)
	transits, landings, offline, stale, pending := 0, 0, 0, 0, 0
	for _, a := range agents {
		if str(a["role"]) == "transit" {
			transits++
		}
		if str(a["role"]) == "landing" {
			landings++
		}
		switch agentLiveness(a) {
		case "offline":
			offline++
		case "stale":
			stale++
		case "pending":
			pending++
		}
	}
	statusIcon := "✅"
	if len(agents) == 0 {
		statusIcon = "⚠️"
	}
	if offline > 0 {
		statusIcon = "🔴"
	} else if stale > 0 {
		statusIcon = "🟡"
	} else if pending > 0 {
		statusIcon = "🕓"
	}
	return strings.Join([]string{
		fmt.Sprintf("%s Hub 管理面正常", statusIcon),
		fmt.Sprintf("节点：%d 个中转 / %d 个落地 / %d 待接入", transits, landings, pending),
		fmt.Sprintf("异常：%d 离线 / %d 可能掉线", offline, stale),
		fmt.Sprintf("待处理任务：%d", taskSummary.Queued),
		"巡检：面板 → 刷新节点详情。",
		"拓扑：面板 → 拓扑。",
	}, "\n")
}

func hubTelegramCommands() string {
	return formatTelegramCommandRows(false, hubTelegramHelpRows)
}

func selectedTelegramCommands(hub bool) []obj {
	if hub {
		return cloneObjList(hubTelegramCommandRows)
	}
	return cloneObjList(telegramCommands)
}

func cloneObjList(in []obj) []obj {
	out := make([]obj, 0, len(in))
	for _, item := range in {
		copyItem := obj{}
		for k, v := range item {
			copyItem[k] = v
		}
		out = append(out, copyItem)
	}
	return out
}

func cloneObjDeep(in obj) obj {
	if in == nil {
		return nil
	}
	out := obj{}
	for k, v := range in {
		out[k] = cloneAnyDeep(v)
	}
	return out
}

func cloneObjListDeep(in []obj) []obj {
	out := make([]obj, 0, len(in))
	for _, item := range in {
		out = append(out, cloneObjDeep(item))
	}
	return out
}

func cloneAnyListDeep(in []any) []any {
	out := make([]any, 0, len(in))
	for _, item := range in {
		out = append(out, cloneAnyDeep(item))
	}
	return out
}

func cloneAnyDeep(v any) any {
	switch x := v.(type) {
	case obj:
		return cloneObjDeep(x)
	case []obj:
		return cloneObjListDeep(x)
	case []any:
		return cloneAnyListDeep(x)
	default:
		return x
	}
}

func formatTelegramCommands(botFather, hub bool) string {
	rows := selectedTelegramCommands(hub)
	return formatTelegramCommandRows(botFather, rows)
}

func formatTelegramCommandRows(botFather bool, rows []obj) string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		if botFather {
			lines = append(lines, fmt.Sprintf("%s - %s", str(row["command"]), str(row["description"])))
			continue
		}
		args := ""
		if str(row["args"]) != "" {
			args = " " + str(row["args"])
		}
		lines = append(lines, fmt.Sprintf("/%s%s — %s", str(row["command"]), args, str(row["description"])))
	}
	return strings.Join(lines, "\n")
}

func normalizeTelegramCommand(raw string) string {
	command := strings.TrimPrefix(strings.Split(raw, "@")[0], "/")
	command = strings.ReplaceAll(command, "-", "_")
	command = strings.ToLower(command)
	return strings.TrimPrefix(command, "relaypilot_")
}

func isRelayPilotTelegramCommand(raw string) bool {
	command := strings.TrimPrefix(strings.Split(raw, "@")[0], "/")
	command = strings.ReplaceAll(command, "-", "_")
	command = strings.ToLower(strings.TrimSpace(command))
	return command == "relaypilot" || strings.HasPrefix(command, "relaypilot_")
}

func formatLabels(labels obj) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+str(labels[k]))
	}
	return strings.Join(parts, ",")
}

func agentLabel(agent obj, key string) string {
	return str(asObj(agent["labels"])[key])
}

func splitLabelValues(value any) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(str(value), ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out[part] = true
		}
	}
	return out
}

func landingMatchesTransit(landing, transit obj) bool {
	landingLabels := asObj(landing["labels"])
	transitLabels := asObj(transit["labels"])
	transitID := str(transit["id"])
	landingID := str(landing["id"])
	for _, key := range []string{"transit", "parent", "upstream"} {
		if splitLabelValues(landingLabels[key])[transitID] {
			return true
		}
	}
	for _, key := range []string{"landing", "landings", "downstream"} {
		if splitLabelValues(transitLabels[key])[landingID] {
			return true
		}
	}
	for _, key := range []string{"route", "group", "region", "site"} {
		left := str(landingLabels[key])
		right := str(transitLabels[key])
		if left != "" && left == right {
			return true
		}
	}
	return false
}

func landingEndpointKeys(landing obj) map[string]bool {
	keys := map[string]bool{}
	labels := asObj(landing["labels"])
	for _, key := range []string{"endpoint", "endpoint_name"} {
		if value := str(labels[key]); value != "" {
			keys["endpoint_name:"+value] = true
		}
	}
	if value := str(labels["tag"]); value != "" {
		keys["tag:"+value] = true
	}
	if server, port := str(labels["server"]), str(labels["server_port"]); server != "" && port != "" {
		keys["server:"+server+":"+port] = true
	}
	for _, raw := range asList(asObj(landing["topology"])["endpoints"]) {
		endpoint := asObj(raw)
		if value := str(endpoint["name"]); value != "" {
			keys["endpoint_name:"+value] = true
		}
		if value := str(endpoint["tag"]); value != "" {
			keys["tag:"+value] = true
		}
		if server, port := str(endpoint["server"]), str(endpoint["server_port"]); server != "" && port != "" {
			keys["server:"+server+":"+port] = true
		}
	}
	return keys
}

func transitLinkMatchesLanding(link, landing obj) bool {
	keys := landingEndpointKeys(landing)
	if value := str(link["endpoint_name"]); value != "" && keys["endpoint_name:"+value] {
		return true
	}
	if value := str(link["outbound_tag"]); value != "" && keys["tag:"+value] {
		return true
	}
	if server, port := str(link["server"]), str(link["server_port"]); server != "" && port != "" && keys["server:"+server+":"+port] {
		return true
	}
	return false
}

func formatHubTopologyText(agents []obj) string {
	if len(agents) == 0 {
		return "🌐 拓扑：暂无 agent。"
	}
	var transits, landings []obj
	for _, agent := range agents {
		switch str(agent["role"]) {
		case "transit":
			transits = append(transits, agent)
		case "landing":
			landings = append(landings, agent)
		}
	}
	sort.Slice(transits, func(i, j int) bool { return str(transits[i]["id"]) < str(transits[j]["id"]) })
	sort.Slice(landings, func(i, j int) bool { return str(landings[i]["id"]) < str(landings[j]["id"]) })
	lines := []string{fmt.Sprintf("🌐 转发拓扑：%d 个中转 / %d 个落地", len(transits), len(landings))}
	if len(transits) == 0 && len(landings) > 0 {
		lines = append(lines, "⚠️ 还没有中转节点，落地节点暂时无法挂到链路下。")
	}
	attached := map[string]bool{}
	for transitIndex, transit := range transits {
		links := asList(asObj(transit["topology"])["links"])
		children := []obj{}
		childLinks := map[string][]obj{}
		for _, landing := range landings {
			var matched []obj
			for _, raw := range links {
				link := asObj(raw)
				if transitLinkMatchesLanding(link, landing) {
					matched = append(matched, link)
				}
			}
			if len(matched) > 0 || landingMatchesTransit(landing, transit) {
				children = append(children, landing)
				childLinks[str(landing["id"])] = matched
				attached[str(landing["id"])] = true
			}
		}
		isLastBranch := transitIndex == len(transits)-1 && len(attached) == len(landings)
		branchPrefix := "├─"
		childPrefix := "│  "
		if isLastBranch {
			branchPrefix = "└─"
			childPrefix = "   "
		}
		lines = append(lines, branchPrefix+" "+formatTopologyAgentLine(transit, roleIcon("transit")))
		if len(children) == 0 {
			lines = append(lines, childPrefix+"└─ ⚠️ 未关联落地")
			continue
		}
		for childIndex, landing := range children {
			childBranch := "├─"
			if childIndex == len(children)-1 {
				childBranch = "└─"
			}
			linkText := ""
			if matched := childLinks[str(landing["id"])]; len(matched) > 0 {
				link := matched[0]
				var linkParts []string
				if str(link["link_mode"]) == "mesh" {
					linkParts = append(linkParts, "mesh")
				}
				if str(link["auth_user"]) != "" {
					linkParts = append(linkParts, "user:"+str(link["auth_user"]))
				} else if str(link["outbound_tag"]) != "" {
					linkParts = append(linkParts, str(link["outbound_tag"]))
				}
				if len(linkParts) > 0 {
					linkText = " ← " + strings.Join(linkParts, " ")
				}
			}
			lines = append(lines, childPrefix+childBranch+" "+formatTopologyAgentLine(landing, roleIcon("landing"))+linkText)
		}
	}
	var orphanLandings []obj
	for _, landing := range landings {
		if !attached[str(landing["id"])] {
			orphanLandings = append(orphanLandings, landing)
		}
	}
	if len(orphanLandings) > 0 {
		lines = append(lines, "└─ ⚠️ 未关联落地")
		for i, landing := range orphanLandings {
			prefix := "   ├─"
			if i == len(orphanLandings)-1 {
				prefix = "   └─"
			}
			lines = append(lines, prefix+" "+formatTopologyAgentLine(landing, roleIcon("landing")))
		}
	}
	lines = append(lines, "🟢 在线  🟡 可能掉线  🔴 离线")
	lines = append(lines, "拓扑优先来自中转配置快照；labels 只做兜底分组。")
	return strings.Join(lines, "\n")
}

func formatTopologyAgentLine(agent obj, icon string) string {
	var parts []string
	for _, key := range []string{"route", "group", "region", "site"} {
		if value := agentLabel(agent, key); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	suffix := ""
	if len(parts) > 0 {
		suffix = " (" + strings.Join(parts, ", ") + ")"
	}
	return fmt.Sprintf("%s %s %s · %s%s", livenessIcon(agent), icon, str(agent["id"]), str(agent["name"]), suffix)
}

func formatHubTasksText(tasks []obj) string {
	if len(tasks) == 0 {
		return "当前没有待处理任务。"
	}
	counts := map[string]int{}
	for _, t := range tasks {
		counts[str(t["status"])]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🕓 任务：%d（queued %d / running %d / done %d / failed %d）\n", len(tasks), counts["queued"], counts["running"], counts["done"], counts["failed"])
	for i, t := range tasks {
		if i >= 20 {
			fmt.Fprintf(&b, "… 还有 %d 个任务未显示。\n", len(tasks)-20)
			break
		}
		args := joinAny(asList(t["args"]))
		fmt.Fprintf(&b, "%s %s %s: /%s %s\n", statusIcon(str(t["status"])), roleIcon(str(t["agent_role"])), str(t["agent_id"]), str(t["command"]), args)
	}
	return strings.TrimRight(b.String(), "\n")
}

func statusIcon(status string) string {
	switch status {
	case "queued":
		return "🕓"
	case "running":
		return "⏳"
	case "done":
		return "✅"
	case "failed":
		return "❌"
	case "cancelled":
		return "🚫"
	default:
		return "•"
	}
}

func joinAny(xs []any) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, str(x))
	}
	return strings.Join(parts, " ")
}

func filterDoneTasks(tasks []obj, batchID string) []obj {
	return filterDoneTasksForAgent(tasks, batchID, "")
}

func filterDoneTasksForAgent(tasks []obj, batchID, agentID string) []obj {
	var out []obj
	for _, task := range tasks {
		status := str(task["status"])
		if status != "done" && status != "failed" && status != "cancelled" {
			continue
		}
		if batchID != "" && str(task["batch_id"]) != batchID {
			continue
		}
		if agentID != "" && str(task["agent_id"]) != agentID {
			continue
		}
		out = append(out, task)
	}
	return out
}

func formatHubTaskResultsText(tasks []obj, batchID string) string {
	return formatHubTaskResultsTextForAgent(tasks, batchID, "")
}

func formatHubTaskResultsTextForAgent(tasks []obj, batchID, agentID string) string {
	selected := filterDoneTasksForAgent(tasks, batchID, agentID)
	if len(selected) == 0 {
		if agentID != "" {
			return "暂无最近操作：" + agentID
		}
		return "暂无最近操作。"
	}
	var b strings.Builder
	if agentID != "" {
		fmt.Fprintf(&b, "📬 最近操作：%s · %d\n", agentID, len(selected))
	} else {
		fmt.Fprintf(&b, "📬 最近操作：%d\n", len(selected))
	}
	for _, task := range selected {
		fmt.Fprintf(&b, "\n%s %s %s /%s\n", statusIcon(str(task["status"])), roleIcon(str(task["agent_role"])), str(task["agent_id"]), str(task["command"]))
		res := asObj(task["result"])
		if str(res["text"]) != "" {
			lines := strings.Split(str(res["text"]), "\n")
			for i, line := range lines {
				if i >= 6 {
					break
				}
				if strings.TrimSpace(line) != "" {
					b.WriteString(line + "\n")
				}
			}
		}
		errText := str(res["error"])
		if errText != "" {
			fmt.Fprintf(&b, "错误：%s\n", errText)
		}
		if hint := taskFailureHint(task, res); hint != "" {
			fmt.Fprintf(&b, "建议：%s\n", hint)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func taskFailureHint(task, res obj) string {
	status := str(task["status"])
	success, hasSuccess := res["success"].(bool)
	if status != "failed" && (!hasSuccess || success) {
		return ""
	}
	command := str(task["command"])
	msg := strings.ToLower(str(res["error"]) + "\n" + str(res["text"]))
	switch {
	case strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind:"):
		return "检查端口占用，或调整对应监听端口。"
	case strings.Contains(msg, "sing-box check failed"):
		return "检查 sing-box 配置片段后重试。"
	case strings.Contains(msg, "remote decommission is disabled") || strings.Contains(msg, "allow_remote_decommission"):
		return "在节点本机开启远程退役授权后重试。"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "no route to host"):
		return "检查目标地址、端口、防火墙和公网入口。"
	case strings.Contains(msg, "task lease expired"):
		return "检查 Agent 服务是否运行，再恢复超时任务。"
	case command == "self_update":
		return "检查版本号、网络访问和节点磁盘空间。"
	case strings.Contains(msg, "unsupported agent task command"):
		return "升级节点 RelayPilot 后重试。"
	default:
		return ""
	}
}

func recentOperationLine(tasks []obj) string {
	if len(tasks) == 0 {
		return "最近操作：暂无"
	}
	for _, task := range tasks {
		status := str(task["status"])
		if status == "" {
			continue
		}
		agentID := str(task["agent_id"])
		command := str(task["command"])
		if agentID == "" && command == "" {
			continue
		}
		label := status
		switch status {
		case "queued":
			label = "等待执行"
		case "running":
			label = "执行中"
		case "done":
			label = "成功"
		case "failed":
			label = "失败"
		case "cancelled":
			label = "已取消"
		}
		target := strings.TrimSpace(agentID + " /" + command)
		return "最近操作：" + target + " · " + label
	}
	return "最近操作：暂无"
}

func batchTasks(tasks []obj, batchID string) []obj {
	var out []obj
	for _, task := range tasks {
		if str(task["batch_id"]) == batchID {
			out = append(out, task)
		}
	}
	return out
}

func batchReady(tasks []obj) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, task := range tasks {
		status := str(task["status"])
		if status != "done" && status != "failed" && status != "cancelled" {
			return false
		}
	}
	return true
}

func pendingBatchExpired(batch obj) bool {
	created := int64Value(batch["created_at"])
	timeout := int64Value(batch["timeout_seconds"])
	if timeout <= 0 {
		timeout = tgBatchResultTimeoutSec
	}
	return created > 0 && now()-created >= timeout
}

func extractBatchID(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "批次：") {
			return strings.TrimSpace(strings.TrimPrefix(line, "批次："))
		}
	}
	return ""
}

func pendingTGBatchesPath(stateDir string) string {
	return filepath.Join(stateDir, "telegram-pending-batches.json")
}

func loadPendingTGBatches(stateDir string) (obj, error) {
	path := pendingTGBatchesPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return obj{"kind": "relaypilot/telegram-pending-batches", "version": version, "batches": obj{}}, nil
	}
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if _, ok := data["batches"].(map[string]any); !ok {
		data["batches"] = obj{}
	}
	return data, nil
}

func savePendingTGBatches(stateDir string, data obj) error {
	data["kind"] = "relaypilot/telegram-pending-batches"
	data["version"] = version
	return writeJSON(pendingTGBatchesPath(stateDir), data, 0o600)
}

func recordPendingTGBatch(stateDir, batchID, originText string, chatID any) error {
	return recordPendingTGBatchTarget(stateDir, batchID, originText, chatID, nil)
}

func recordPendingTGBatchTarget(stateDir, batchID, originText string, chatID, messageID any) error {
	if batchID == "" {
		return nil
	}
	data, err := loadPendingTGBatches(stateDir)
	if err != nil {
		return err
	}
	batches := asObj(data["batches"])
	if len(batches) >= maxPendingTGBatches {
		type candidate struct {
			id string
			ts int64
		}
		var candidates []candidate
		for id, raw := range batches {
			candidates = append(candidates, candidate{id: id, ts: int64Value(asObj(raw)["created_at"])})
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ts < candidates[j].ts })
		for len(batches) >= maxPendingTGBatches && len(candidates) > 0 {
			delete(batches, candidates[0].id)
			candidates = candidates[1:]
		}
	}
	batch := obj{
		"batch_id":        batchID,
		"origin_text":     originText,
		"chat_id":         chatID,
		"created_at":      now(),
		"timeout_seconds": tgBatchResultTimeoutSec,
	}
	if int64Value(messageID) > 0 {
		batch["message_id"] = int64Value(messageID)
	}
	batches[batchID] = batch
	data["batches"] = batches
	return savePendingTGBatches(stateDir, data)
}

func pendingTGBatchResult(batch obj, batchID, text string) obj {
	out := obj{
		"batch_id":    batchID,
		"chat_id":     batch["chat_id"],
		"origin_text": batch["origin_text"],
		"text":        text,
	}
	if int64Value(batch["message_id"]) > 0 {
		out["message_id"] = int64Value(batch["message_id"])
	}
	return out
}

func collectReadyPendingTGBatches(stateDir string) ([]obj, error) {
	data, err := loadPendingTGBatches(stateDir)
	if err != nil {
		return nil, err
	}
	tasks, err := listHubTasks(stateDir)
	if err != nil {
		return nil, err
	}
	batches := asObj(data["batches"])
	ready := []obj{}
	changed := false
	for batchID, raw := range batches {
		batch := asObj(raw)
		matched := batchTasks(tasks, batchID)
		if len(matched) == 0 {
			if pendingBatchExpired(batch) {
				ready = append(ready, pendingTGBatchResult(batch, batchID, fmt.Sprintf("⏱️ 最近操作超时：%s\n没有找到该批次任务。", batchID)))
				delete(batches, batchID)
				changed = true
			}
			continue
		}
		if batchReady(matched) {
			ready = append(ready, pendingTGBatchResult(batch, batchID, formatHubTaskResultsText(tasks, batchID)))
			delete(batches, batchID)
			changed = true
			continue
		}
		if pendingBatchExpired(batch) {
			ready = append(ready, pendingTGBatchResult(batch, batchID, "⏱️ 部分任务仍未完成，当前最近操作：\n"+formatHubTaskResultsText(tasks, batchID)))
			delete(batches, batchID)
			changed = true
		}
	}
	if changed {
		data["batches"] = batches
		if err := savePendingTGBatches(stateDir, data); err != nil {
			return nil, err
		}
	}
	return ready, nil
}

func formatHubAlertsText(alerts []obj) string {
	if len(alerts) == 0 {
		return "当前没有离线节点告警。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🚨 离线告警：%d\n", len(alerts))
	for _, alert := range alerts {
		agent := asObj(alert["agent"])
		hours := int(int64Value(alert["offline_age_seconds"]) / 3600)
		fmt.Fprintf(&b, "- %s · %s · %s · 离线约 %dh\n", str(alert["agent_id"]), str(agent["name"]), str(alert["status"]), hours)
	}
	return strings.TrimRight(b.String(), "\n")
}

func offlineAge(agent obj, n int64) int64 {
	last := int64Value(firstNonNil(agent["last_seen"], agent["updated_at"]))
	if last <= 0 {
		return -1
	}
	age := n - last
	if age < 0 {
		return 0
	}
	return age
}

func offlineAlertMessage(agent obj, ageSeconds int64) string {
	role := "节点"
	if str(agent["role"]) == "transit" {
		role = "中转"
	} else if str(agent["role"]) == "landing" {
		role = "落地"
	}
	name := firstNonEmpty(str(agent["name"]), str(agent["id"]))
	hours := int(ageSeconds / 3600)
	return strings.Join([]string{
		"🔴 节点长时间失联",
		fmt.Sprintf("%s：%s (%s)", role, name, str(agent["id"])),
		fmt.Sprintf("失联：约 %d 小时", hours),
		"",
		"请选择处理方式：删除节点，或继续观察 24 小时。",
	}, "\n")
}

func offlineAlertReplyMarkup(removeToken, observeToken string) obj {
	return obj{"inline_keyboard": []any{[]any{
		obj{"text": "删除节点", "callback_data": "rp:rm:" + removeToken},
		obj{"text": "继续观察", "callback_data": "rp:obs:" + observeToken},
	}}}
}

func newAlertToken() (string, error) {
	token, err := randomToken(8)
	if err != nil {
		return "", err
	}
	return token, nil
}

func alertShouldSend(alert obj, n int64) bool {
	if len(alert) == 0 {
		return true
	}
	status := str(alert["status"])
	if status == "removed" || status == "resolved" {
		return false
	}
	snoozeUntil := int64Value(alert["snooze_until"])
	if snoozeUntil > 0 && n < snoozeUntil {
		return false
	}
	nextAlertAt := int64Value(alert["next_alert_at"])
	return n >= nextAlertAt
}

func scanOfflineAgentAlerts(stateDir string, thresholdSeconds, snoozeSeconds int64, dryRun bool) (obj, error) {
	if thresholdSeconds < 1 {
		thresholdSeconds = offlineAlertSeconds
	}
	if snoozeSeconds < 1 {
		snoozeSeconds = offlineAlertSnoozeSecs
	}
	n := now()
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return nil, err
	}
	alertsData, err := loadHubAlertsData(stateDir)
	if err != nil {
		return nil, err
	}
	alerts := asObj(alertsData["alerts"])
	sent := []any{}
	skipped := []any{}
	for _, agent := range agents {
		agentID := str(agent["id"])
		age := offlineAge(agent, n)
		if age < thresholdSeconds {
			continue
		}
		existing := asObj(alerts[agentID])
		if !alertShouldSend(existing, n) {
			skipped = append(skipped, obj{"agent_id": agentID, "reason": "snoozed_or_recent", "offline_age_seconds": age})
			continue
		}
		removeToken := str(existing["remove_token"])
		if removeToken == "" {
			removeToken, err = newAlertToken()
			if err != nil {
				return nil, err
			}
		}
		observeToken := str(existing["observe_token"])
		if observeToken == "" {
			observeToken, err = newAlertToken()
			if err != nil {
				return nil, err
			}
		}
		message := offlineAlertMessage(agent, age)
		replyMarkup := offlineAlertReplyMarkup(removeToken, observeToken)
		apiResult := obj{"dry_run": dryRun}
		if dryRun {
			apiResult["method"] = "sendMessage"
			apiResult["payload"] = obj{"text": message, "reply_markup": replyMarkup}
		} else {
			cfg, err := loadTelegramConfig(stateDir)
			if err != nil {
				return nil, err
			}
			apiResult, err = telegramAPIRequest(cfg, "sendMessage", obj{
				"chat_id":      cfg["chat_id"],
				"text":         message,
				"reply_markup": replyMarkup,
			}, 10*time.Second)
			if err != nil {
				return nil, err
			}
		}
		alerts[agentID] = obj{
			"agent_id":            agentID,
			"status":              "alerted",
			"agent":               summarizeAgent(agent),
			"offline_age_seconds": age,
			"threshold_seconds":   thresholdSeconds,
			"alerted_at":          n,
			"updated_at":          n,
			"next_alert_at":       n + snoozeSeconds,
			"remove_token":        removeToken,
			"observe_token":       observeToken,
			"dry_run":             dryRun,
		}
		sent = append(sent, obj{"agent_id": agentID, "offline_age_seconds": age, "api": apiResult})
	}
	alertsData["alerts"] = alerts
	if len(sent) > 0 && !dryRun {
		if err := saveHubAlertsData(stateDir, alertsData); err != nil {
			return nil, err
		}
	}
	return obj{"sent": sent, "skipped": skipped, "threshold_seconds": thresholdSeconds, "dry_run": dryRun}, nil
}

func handleHubAlertCallback(stateDir, data string, snoozeSeconds int64) (obj, error) {
	if snoozeSeconds < 1 {
		snoozeSeconds = offlineAlertSnoozeSecs
	}
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "rp" || (parts[1] != "rm" && parts[1] != "obs") {
		return nil, fmt.Errorf("unsupported callback data: %q", data)
	}
	action, token := parts[1], parts[2]
	alertsData, err := loadHubAlertsData(stateDir)
	if err != nil {
		return nil, err
	}
	alerts := asObj(alertsData["alerts"])
	matchedID := ""
	var matched obj
	for agentID, raw := range alerts {
		alert := asObj(raw)
		if action == "rm" && str(alert["remove_token"]) == token {
			matchedID, matched = agentID, alert
			break
		}
		if action == "obs" && str(alert["observe_token"]) == token {
			matchedID, matched = agentID, alert
			break
		}
	}
	if matchedID == "" || len(matched) == 0 {
		return nil, errors.New("callback token not found or expired")
	}
	n := now()
	if action == "rm" {
		result, err := removeHubAgent(stateDir, matchedID, "tg-offline-alert", true, true)
		if err != nil {
			return nil, err
		}
		matched["status"] = "removed"
		matched["resolved_at"] = n
		matched["updated_at"] = n
		matched["remove_result"] = result
		alerts[matchedID] = matched
		alertsData["alerts"] = alerts
		if err := saveHubAlertsData(stateDir, alertsData); err != nil {
			return nil, err
		}
		return obj{"action": "removed", "agent_id": matchedID, "message": "已删除节点 " + matchedID, "result": result}, nil
	}
	matched["status"] = "observing"
	matched["snooze_until"] = n + snoozeSeconds
	matched["updated_at"] = n
	alerts[matchedID] = matched
	alertsData["alerts"] = alerts
	if err := saveHubAlertsData(stateDir, alertsData); err != nil {
		return nil, err
	}
	return obj{
		"action":       "observing",
		"agent_id":     matchedID,
		"snooze_until": matched["snooze_until"],
		"message":      fmt.Sprintf("继续观察节点 %s，%d 小时后仍失联会再次提醒。", matchedID, snoozeSeconds/3600),
	}, nil
}
