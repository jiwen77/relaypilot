package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func telegramConfigPath(stateDir string) string { return filepath.Join(stateDir, telegramConfigName) }
func telegramCallbackMapPath(stateDir string) string {
	return filepath.Join(stateDir, "telegram-callback-map.json")
}

var telegramIPv4RE = regexp.MustCompile(`(^|[^0-9])([0-9]{1,3}\.[0-9]{1,3})\.[0-9]{1,3}\.[0-9]{1,3}([^0-9]|$)`)
var telegramBareIPv4RE = regexp.MustCompile(`(?m)(^|[^0-9])(?:[0-9]{1,3}\.){3}[0-9]{1,3}([^0-9]|$)`)

func maskTelegramIPs(text string) string {
	return telegramIPv4RE.ReplaceAllString(text, `${1}${2}.*.*${3}`)
}

func containsTelegramIPv4(text string) bool {
	return telegramBareIPv4RE.MatchString(text)
}

func maskSecret(value string, keep int) string {
	if value == "" {
		return ""
	}
	if keep <= 0 {
		keep = 4
	}
	runes := []rune(value)
	if len(runes) <= keep*2 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:keep]) + "..." + string(runes[len(runes)-keep:])
}

func configureTelegram(stateDir, botToken, chatID, apiBase string, enabled bool) (string, error) {
	if botToken == "" {
		return "", errors.New("telegram bot token is required")
	}
	if chatID == "" {
		return "", errors.New("telegram chat id is required")
	}
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	cfg := obj{
		"enabled":   enabled,
		"bot_token": botToken,
		"chat_id":   chatID,
		"api_base":  strings.TrimRight(apiBase, "/"),
		"commands":  selectedTelegramCommands(false),
	}
	path := telegramConfigPath(stateDir)
	return path, writeJSON(path, cfg, 0o600)
}

func loadTelegramConfig(stateDir string) (obj, error) {
	cfg, err := loadJSON(telegramConfigPath(stateDir))
	if err != nil {
		return nil, err
	}
	if str(cfg["bot_token"]) == "" {
		return nil, errors.New("telegram config missing bot_token")
	}
	if str(cfg["chat_id"]) == "" {
		return nil, errors.New("telegram config missing chat_id")
	}
	if str(cfg["api_base"]) == "" {
		cfg["api_base"] = "https://api.telegram.org"
	}
	if cfg["enabled"] == nil {
		cfg["enabled"] = true
	}
	if enabled, ok := cfg["enabled"].(bool); ok && !enabled {
		return nil, errors.New("telegram config is disabled")
	}
	cfg["_state_dir"] = stateDir
	return cfg, nil
}

func telegramStatus(stateDir string) obj {
	path := telegramConfigPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return obj{"configured": false, "path": path}
	}
	cfg, err := loadJSON(path)
	if err != nil {
		return obj{"configured": false, "path": path, "error": err.Error()}
	}
	if cfg["enabled"] == nil {
		cfg["enabled"] = true
	}
	if str(cfg["api_base"]) == "" {
		cfg["api_base"] = "https://api.telegram.org"
	}
	return obj{
		"configured": true,
		"path":       path,
		"enabled":    cfg["enabled"],
		"chat_id":    str(cfg["chat_id"]),
		"bot_token":  maskSecret(str(cfg["bot_token"]), 4),
		"api_base":   cfg["api_base"],
		"commands":   selectedTelegramCommands(false),
	}
}

func telegramAPIRequest(cfg obj, method string, payload obj, timeout time.Duration) (obj, error) {
	token := str(cfg["bot_token"])
	if token == "" {
		return nil, errors.New("telegram bot token is required")
	}
	payload = sanitizeTelegramPayload(cfg, payload)
	form := url.Values{}
	for key, value := range payload {
		if value == nil {
			continue
		}
		switch v := value.(type) {
		case obj, []any, []obj:
			wire, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			form.Set(key, string(wire))
		case bool:
			if v {
				form.Set(key, "true")
			} else {
				form.Set(key, "false")
			}
		default:
			form.Set(key, str(v))
		}
	}
	body := []byte(form.Encode())
	if len(body) > maxHTTPBodyBytes {
		return nil, fmt.Errorf("telegram payload too large: %d bytes > %d", len(body), maxHTTPBodyBytes)
	}
	apiBase := strings.TrimRight(str(cfg["api_base"]), "/")
	req, err := http.NewRequest("POST", apiBase+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: timeout + 5*time.Second}
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
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("telegram returned non-json response: %q", string(data[:minInt(len(data), 200)]))
		}
	}
	if resp.StatusCode >= 400 || out["ok"] == false {
		return nil, fmt.Errorf("telegram %s failed (%d): %s", method, resp.StatusCode, str(firstNonNil(out["description"], out["error"])))
	}
	return out, nil
}

func telegramBotCommandsPayload(hub bool) []any {
	rows := selectedTelegramCommands(hub)
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, obj{"command": row["command"], "description": row["description"]})
	}
	return out
}

func parseBotCommandScope(scope string) (obj, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil, nil
	}
	if scope == "default" {
		return obj{"type": "default"}, nil
	}
	if strings.HasPrefix(scope, "{") {
		var out obj
		if err := json.Unmarshal([]byte(scope), &out); err != nil {
			return nil, fmt.Errorf("invalid telegram command scope JSON: %w", err)
		}
		if str(out["type"]) == "" {
			return nil, errors.New("telegram command scope JSON must be an object with type")
		}
		return out, nil
	}
	switch scope {
	case "all_private_chats", "all_group_chats", "all_chat_administrators":
		return obj{"type": scope}, nil
	}
	prefix, rest, ok := strings.Cut(scope, ":")
	if ok && (prefix == "chat" || prefix == "chat_administrators") && rest != "" {
		return obj{"type": prefix, "chat_id": rest}, nil
	}
	if ok && prefix == "chat_member" && rest != "" {
		chatID, userID, ok := strings.Cut(rest, ":")
		if ok && chatID != "" && userID != "" {
			parsedUserID, err := strconv.ParseInt(userID, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid chat_member user_id: %s", userID)
			}
			return obj{"type": "chat_member", "chat_id": chatID, "user_id": parsedUserID}, nil
		}
	}
	return nil, fmt.Errorf("unsupported telegram command scope: %q", scope)
}

func telegramWirePayload(payload obj) obj {
	out := obj{}
	for key, value := range payload {
		if value == nil {
			continue
		}
		switch v := value.(type) {
		case obj, []any, []obj:
			wire, _ := json.Marshal(v)
			out[key] = string(wire)
		case bool:
			if v {
				out[key] = "true"
			} else {
				out[key] = "false"
			}
		default:
			out[key] = str(v)
		}
	}
	return out
}

func storeTelegramCallbackData(stateDir, data string) string {
	data = strings.TrimSpace(data)
	if data == "" || strings.HasPrefix(data, "tgcb:") || !containsTelegramIPv4(data) {
		return data
	}
	if stateDir == "" {
		return maskTelegramIPs(data)
	}
	sum := sha256.Sum256([]byte(data))
	token := "tgcb:" + fmt.Sprintf("%x", sum)[:24]
	path := telegramCallbackMapPath(stateDir)
	callbacks := obj{}
	if existing, err := loadJSON(path); err == nil {
		callbacks = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		callbacks = obj{}
	}
	callbacks[token] = obj{"v": data, "ts": now()}
	_ = writeJSON(path, callbacks, 0o600)
	return token
}

func resolveTelegramCallbackData(stateDir, data string) string {
	if !strings.HasPrefix(data, "tgcb:") || stateDir == "" {
		return data
	}
	callbacks, err := loadJSON(telegramCallbackMapPath(stateDir))
	if err != nil {
		return data
	}
	rec := callbacks[data]
	if m := asObj(rec); len(m) > 0 {
		if value := str(m["v"]); value != "" {
			return value
		}
	}
	if value := str(rec); value != "" {
		return value
	}
	return data
}

func sanitizeTelegramPayload(cfg obj, payload obj) obj {
	out := obj{}
	for key, value := range payload {
		out[key] = sanitizeTelegramValue(cfg, key, value)
	}
	return out
}

func sanitizeTelegramValue(cfg obj, key string, value any) any {
	switch v := value.(type) {
	case obj:
		out := obj{}
		for childKey, childValue := range v {
			out[childKey] = sanitizeTelegramValue(cfg, childKey, childValue)
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, childValue := range v {
			out = append(out, sanitizeTelegramValue(cfg, "", childValue))
		}
		return out
	case []obj:
		out := make([]obj, 0, len(v))
		for _, childValue := range v {
			out = append(out, sanitizeTelegramValue(cfg, "", childValue).(obj))
		}
		return out
	case string:
		switch key {
		case "text", "caption":
			return maskTelegramIPs(v)
		case "url":
			return maskTelegramIPs(v)
		case "callback_data":
			return storeTelegramCallbackData(str(cfg["_state_dir"]), v)
		case "reply_markup":
			var decoded any
			if err := json.Unmarshal([]byte(v), &decoded); err == nil {
				wire, err := json.Marshal(sanitizeTelegramValue(cfg, key, decoded))
				if err == nil {
					return string(wire)
				}
			}
			return maskTelegramIPs(v)
		default:
			return v
		}
	default:
		return value
	}
}

func telegramAPICall(stateDir, method string, payload obj, dryRun bool) (obj, error) {
	cfg, err := loadTelegramConfig(stateDir)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		payload = obj{}
	}
	urlText := strings.TrimRight(str(cfg["api_base"]), "/") + "/bot" + str(cfg["bot_token"]) + "/" + method
	if dryRun {
		payload = sanitizeTelegramPayload(cfg, payload)
		return obj{
			"dry_run": true,
			"method":  method,
			"url":     strings.ReplaceAll(urlText, str(cfg["bot_token"]), maskSecret(str(cfg["bot_token"]), 4)),
			"payload": telegramWirePayload(payload),
		}, nil
	}
	resp, err := telegramAPIRequest(cfg, method, payload, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return obj{"dry_run": false, "method": method, "response": resp}, nil
}

func registerTelegramCommands(stateDir, scope, languageCode string, hub, dryRun bool) (obj, error) {
	payload := obj{"commands": telegramBotCommandsPayload(hub)}
	parsedScope, err := parseBotCommandScope(scope)
	if err != nil {
		return nil, err
	}
	if parsedScope != nil {
		payload["scope"] = parsedScope
	}
	if languageCode != "" {
		payload["language_code"] = languageCode
	}
	return telegramAPICall(stateDir, "setMyCommands", payload, dryRun)
}

func remoteTelegramCommands(stateDir, method, scope, languageCode string, dryRun bool) (obj, error) {
	payload := obj{}
	parsedScope, err := parseBotCommandScope(scope)
	if err != nil {
		return nil, err
	}
	if parsedScope != nil {
		payload["scope"] = parsedScope
	}
	if languageCode != "" {
		payload["language_code"] = languageCode
	}
	return telegramAPICall(stateDir, method, payload, dryRun)
}

func sendTelegramMessage(cfg obj, text string, timeout time.Duration) error {
	text = truncateTelegramText(text)
	return sendTelegramMessageTo(cfg, cfg["chat_id"], text, timeout)
}

func sendTelegramMessageCLI(stateDir, text, parseMode string, dryRun bool) (obj, error) {
	if text == "" {
		return nil, errors.New("--text is required")
	}
	cfg, err := loadTelegramConfig(stateDir)
	if err != nil {
		return nil, err
	}
	payload := obj{
		"chat_id":                  cfg["chat_id"],
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if dryRun {
		return telegramAPICall(stateDir, "sendMessage", payload, true)
	}
	resp, err := telegramAPIRequest(cfg, "sendMessage", payload, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return obj{"dry_run": false, "response": resp}, nil
}

func sendTelegramMessageTo(cfg obj, chatID any, text string, timeout time.Duration) error {
	text = truncateTelegramText(text)
	if str(chatID) == "" {
		chatID = cfg["chat_id"]
	}
	_, err := telegramAPIRequest(cfg, "sendMessage", obj{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}, timeout)
	return err
}

type telegramReply struct {
	Text         string
	ParseMode    string
	ReplyMarkup  obj
	CallbackText string
}

func sendTelegramReplyTo(cfg obj, chatID any, reply telegramReply, timeout time.Duration) error {
	if str(chatID) == "" {
		chatID = cfg["chat_id"]
	}
	text := truncateTelegramText(reply.Text)
	if text == "" {
		return nil
	}
	payload := obj{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if reply.ParseMode != "" {
		payload["parse_mode"] = reply.ParseMode
	}
	if len(reply.ReplyMarkup) > 0 {
		payload["reply_markup"] = reply.ReplyMarkup
	}
	_, err := telegramAPIRequest(cfg, "sendMessage", payload, timeout)
	return err
}

func answerTelegramCallback(cfg obj, callbackID, text string, timeout time.Duration) error {
	if callbackID == "" {
		return nil
	}
	_, err := telegramAPIRequest(cfg, "answerCallbackQuery", obj{
		"callback_query_id": callbackID,
		"text":              truncateTelegramText(text),
		"show_alert":        false,
	}, timeout)
	return err
}

func truncateTelegramText(text string) string {
	if len([]byte(text)) <= maxTelegramTextBytes {
		return text
	}
	var b strings.Builder
	for _, r := range text {
		if b.Len()+len(string(r)) > maxTelegramTextBytes-32 {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "\n… truncated"
}

func telegramUpdateChatID(update obj) string {
	if message := asObj(update["message"]); len(message) > 0 {
		return str(asObj(message["chat"])["id"])
	}
	if cb := asObj(update["callback_query"]); len(cb) > 0 {
		if message := asObj(cb["message"]); len(message) > 0 {
			return str(asObj(message["chat"])["id"])
		}
	}
	return ""
}

func telegramUpdateAllowed(cfg obj, update obj) bool {
	expected := strings.TrimSpace(str(cfg["chat_id"]))
	actual := strings.TrimSpace(telegramUpdateChatID(update))
	return expected != "" && actual != "" && expected == actual
}

func tgButton(text, callbackData string) obj {
	return obj{"text": text, "callback_data": callbackData}
}

func tgURLButton(text, rawURL string) obj {
	return obj{"text": text, "url": rawURL}
}

func tgKeyboard(rows ...[]any) obj {
	keyboard := make([]any, 0, len(rows))
	for _, row := range rows {
		keyboard = append(keyboard, row)
	}
	return obj{"inline_keyboard": keyboard}
}

func tgBackKeyboard() obj {
	return tgKeyboard([]any{tgButton("⬅️ 返回总控", "rp:panel")})
}

func tgButtonIfSafe(text, callbackData string) obj {
	if len([]byte(callbackData)) > 64 {
		return nil
	}
	return tgButton(text, callbackData)
}

func appendTGRow(rows *[][]any, buttons ...obj) {
	row := make([]any, 0, len(buttons))
	for _, button := range buttons {
		if button != nil {
			row = append(row, button)
		}
	}
	if len(row) > 0 {
		*rows = append(*rows, row)
	}
}

func tgKeyboardFromButtons(buttons ...obj) obj {
	rows := [][]any{}
	appendTGRow(&rows, buttons...)
	return tgKeyboard(rows...)
}

func htmlCode(text string) string {
	return "<code>" + html.EscapeString(text) + "</code>"
}

func hubPanelReply(stateDir string) telegramReply {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return telegramReply{Text: "❌ " + err.Error()}
	}
	taskSummary, _ := cachedHubTaskSummary(stateDir)
	host, _ := os.Hostname()
	if host == "" {
		host = "hub"
	}
	transits, landings, online, stale, offline, pending := 0, 0, 0, 0, 0, 0
	for _, agent := range agents {
		switch str(agent["role"]) {
		case "transit":
			transits++
		case "landing":
			landings++
		}
		switch agentLiveness(agent) {
		case "online":
			online++
		case "stale":
			stale++
		case "offline":
			offline++
		case "pending":
			pending++
		}
	}
	lines := []string{
		"🛡 <b>RelayPilot 控制中枢</b>",
		"Hub：" + htmlCode(host),
		"当前版本：" + htmlCode(buildVersion),
		fmt.Sprintf("节点：%d 在线 / %d 待接入 / %d 可能掉线 / %d 离线", online, pending, stale, offline),
		fmt.Sprintf("Transit：%d · Landing：%d", transits, landings),
		fmt.Sprintf("待处理任务：%d", taskSummary.Queued),
		firstNonEmpty(taskSummary.RecentLine, "最近操作：暂无"),
	}
	return telegramReply{
		Text:      strings.Join(lines, "\n"),
		ParseMode: "HTML",
		ReplyMarkup: tgKeyboard(
			[]any{tgButton("📊 节点列表", "rp:agents"), tgButton("🌐 拓扑", "rp:topology")},
			[]any{tgButton("📬 最近操作", "rp:results"), tgButton("⬆️ 更新中心", "rp:update")},
		),
		CallbackText: "已刷新",
	}
}

func agentListReply(stateDir string) telegramReply {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	rows := [][]any{}
	for _, agent := range agents {
		agentID := str(agent["id"])
		if agentID == "" {
			continue
		}
		appendTGRow(&rows, tgButtonIfSafe(roleIcon(str(agent["role"]))+" "+agentID, "rp:agent:"+agentID))
	}
	appendTGRow(&rows, tgButton("🔁 刷新节点详情", "rp:sync_all"))
	appendTGRow(&rows, tgButton("⬅️ 返回总控", "rp:panel"))
	return telegramReply{
		Text:         formatAgentsText(agents),
		ReplyMarkup:  tgKeyboard(rows...),
		CallbackText: "节点列表",
	}
}

func agentDetailReply(stateDir, agentID string) telegramReply {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	agent, err := findHubAgentByID(agents, agentID)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	name := firstNonEmpty(str(agent["name"]), "未命名")
	network := firstNonEmpty(agentNetworkLabel(agent), "未刷新")
	labels := formatLabels(asObj(agent["labels"]))
	if labels == "" {
		labels = "无"
	}
	lines := []string{
		roleIcon(str(agent["role"])) + " <b>" + html.EscapeString(agentID) + "</b>",
		"",
		"ID：" + htmlCode(agentID),
		"角色：" + html.EscapeString(roleLabel(str(agent["role"]))),
		"名称：" + html.EscapeString(name),
		"状态：" + html.EscapeString(livenessLabel(agent)),
		"标签：" + html.EscapeString(labels),
		"网络：" + html.EscapeString(network),
		"详情：" + html.EscapeString(agentSyncLabel(agent)),
	}
	rows := [][]any{}
	appendTGRow(&rows,
		tgButtonIfSafe("🔁 刷新详情", "rp:agent:sync:"+agentID),
		tgButtonIfSafe("🩺 Doctor", "rp:agent:doctor:"+agentID),
	)
	appendTGRow(&rows,
		tgButtonIfSafe("🔗 关联节点", "rp:agent:related:"+agentID),
		tgButtonIfSafe("📬 最近操作", "rp:agent:results:"+agentID),
	)
	appendTGRow(&rows,
		tgButtonIfSafe("🧯 退役节点", "rp:agent:retire:"+agentID),
	)
	appendTGRow(&rows, tgButton("⬅️ 返回节点列表", "rp:agents"))
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgKeyboard(rows...),
		CallbackText: "节点详情",
	}
}

type relatedAgent struct {
	agent    obj
	links    []obj
	inferred bool
}

func relatedAgentsForAgent(agents []obj, target obj) []relatedAgent {
	targetID := str(target["id"])
	targetRole := str(target["role"])
	var out []relatedAgent
	for _, candidate := range agents {
		if str(candidate["id"]) == "" || str(candidate["id"]) == targetID {
			continue
		}
		switch {
		case targetRole == "transit" && str(candidate["role"]) == "landing":
			links := matchingTransitLinksForLanding(target, candidate)
			inferred := landingMatchesTransit(candidate, target)
			if len(links) > 0 || inferred {
				out = append(out, relatedAgent{agent: candidate, links: links, inferred: inferred})
			}
		case targetRole == "landing" && str(candidate["role"]) == "transit":
			links := matchingTransitLinksForLanding(candidate, target)
			inferred := landingMatchesTransit(target, candidate)
			if len(links) > 0 || inferred {
				out = append(out, relatedAgent{agent: candidate, links: links, inferred: inferred})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return str(out[i].agent["id"]) < str(out[j].agent["id"]) })
	return out
}

func cloneRelatedAgents(in []relatedAgent) []relatedAgent {
	out := make([]relatedAgent, 0, len(in))
	for _, rel := range in {
		out = append(out, relatedAgent{
			agent:    cloneObjDeep(rel.agent),
			links:    cloneObjListDeep(rel.links),
			inferred: rel.inferred,
		})
	}
	return out
}

func cloneRelatedMap(in map[string][]relatedAgent) map[string][]relatedAgent {
	out := make(map[string][]relatedAgent, len(in))
	for key, value := range in {
		out[key] = cloneRelatedAgents(value)
	}
	return out
}

func buildHubTopologyIndex(signature string, agents []obj) hubTopologyIndex {
	related := map[string][]relatedAgent{}
	for _, agent := range agents {
		agentID := str(agent["id"])
		if agentID == "" {
			continue
		}
		related[agentID] = relatedAgentsForAgent(agents, agent)
	}
	return hubTopologyIndex{
		signature:    signature,
		topologyText: formatHubTopologyText(agents),
		related:      related,
		cachedAt:     time.Now(),
	}
}

func getHubTopologyIndex(stateDir string) (hubTopologyIndex, error) {
	signature, err := hubAgentsFileSignature(stateDir)
	if err != nil {
		return hubTopologyIndex{}, err
	}
	hubViewCache.Lock()
	if cached, ok := hubViewCache.topology[stateDir]; ok && cached.signature == signature && hubCacheFresh(cached.cachedAt, hubTopologyCacheMaxAge) {
		out := hubTopologyIndex{
			signature:    cached.signature,
			topologyText: cached.topologyText,
			related:      cloneRelatedMap(cached.related),
			cachedAt:     cached.cachedAt,
		}
		hubViewCache.Unlock()
		return out, nil
	}
	hubViewCache.Unlock()
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return hubTopologyIndex{}, err
	}
	index := buildHubTopologyIndex(signature, agents)
	hubViewCache.Lock()
	hubViewCache.topology[stateDir] = hubTopologyIndex{
		signature:    index.signature,
		topologyText: index.topologyText,
		related:      cloneRelatedMap(index.related),
		cachedAt:     index.cachedAt,
	}
	hubViewCache.Unlock()
	return index, nil
}

func cachedHubTopologyText(stateDir string) (string, error) {
	index, err := getHubTopologyIndex(stateDir)
	if err != nil {
		return "", err
	}
	return index.topologyText, nil
}

func cachedRelatedAgentsForAgent(stateDir, agentID string) ([]relatedAgent, error) {
	index, err := getHubTopologyIndex(stateDir)
	if err != nil {
		return nil, err
	}
	return cloneRelatedAgents(index.related[agentID]), nil
}

func relatedLinkLabel(rel relatedAgent) string {
	if len(rel.links) == 0 {
		if rel.inferred {
			return "标签关联"
		}
		return ""
	}
	link := rel.links[0]
	parts := []string{formatLinkMode(firstNonEmpty(str(link["link_mode"]), "direct"))}
	if user := str(link["auth_user"]); user != "" {
		parts = append(parts, "user:"+user)
	} else if tag := str(link["outbound_tag"]); tag != "" {
		parts = append(parts, tag)
	}
	if endpoint := str(link["endpoint_name"]); endpoint != "" {
		parts = append(parts, "endpoint:"+endpoint)
	}
	if len(rel.links) > 1 {
		parts = append(parts, fmt.Sprintf("共 %d 条", len(rel.links)))
	}
	return strings.Join(parts, " · ")
}

func agentRelatedReply(stateDir, agentID string) telegramReply {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	if _, err := findHubAgentByID(agents, agentID); err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	related, err := cachedRelatedAgentsForAgent(stateDir, agentID)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	lines := []string{
		"🔗 <b>关联节点</b>",
		"当前：" + htmlCode(agentID),
	}
	rows := [][]any{}
	if len(related) == 0 {
		lines = append(lines, "", "暂无关联节点。")
	} else {
		for _, rel := range related {
			relatedID := str(rel.agent["id"])
			lines = append(lines, "", fmt.Sprintf("%s %s %s · %s · %s", livenessIcon(rel.agent), roleIcon(str(rel.agent["role"])), relatedID, roleLabel(str(rel.agent["role"])), firstNonEmpty(str(rel.agent["name"]), "未命名")))
			if label := relatedLinkLabel(rel); label != "" {
				lines = append(lines, "   "+label)
			}
			appendTGRow(&rows, tgButtonIfSafe(roleIcon(str(rel.agent["role"]))+" "+relatedID, "rp:agent:"+relatedID))
		}
	}
	appendTGRow(&rows, tgButtonIfSafe("⬅️ 返回节点", "rp:agent:"+agentID))
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgKeyboard(rows...),
		CallbackText: "关联节点",
	}
}

func agentTaskResultsReply(stateDir, agentID string) telegramReply {
	text, err := cachedHubTaskResultsText(stateDir, "", agentID)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	return telegramReply{
		Text:         text,
		ReplyMarkup:  tgKeyboardFromButtons(tgButtonIfSafe("⬅️ 返回节点", "rp:agent:"+agentID)),
		CallbackText: "最近操作",
	}
}

func agentRetireReply(stateDir, agentID string) telegramReply {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	agent, err := findHubAgentByID(agents, agentID)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	lines := []string{
		"🧯 <b>退役节点</b>",
		"目标：" + htmlCode(agentID),
		"角色：" + html.EscapeString(roleLabel(str(agent["role"]))),
		"",
		"选择退役方式；确认后才执行。",
		"节点需开启 allow_remote_decommission=true。",
	}
	rows := [][]any{}
	appendTGRow(&rows, tgButtonIfSafe("退出 Hub 托管", "rp:agent:retire-mode:detach:"+agentID))
	appendTGRow(&rows, tgButtonIfSafe("清理托管代理", "rp:agent:retire-mode:purge-managed-proxy:"+agentID))
	appendTGRow(&rows, tgButtonIfSafe("彻底卸载", "rp:agent:retire-mode:uninstall:"+agentID))
	appendTGRow(&rows, tgButtonIfSafe("⬅️ 返回节点", "rp:agent:"+agentID))
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgKeyboard(rows...),
		CallbackText: "退役节点",
	}
}

func agentRetireConfirmReply(agentID, mode string) telegramReply {
	mode, err := normalizeDecommissionMode(mode)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	lines := []string{
		"🧯 <b>确认退役</b>",
		"目标：" + htmlCode(agentID),
		"模式：" + html.EscapeString(decommissionModeLabel(mode)),
		"",
		"确认后 Hub 下发远程清理任务。",
		"彻底卸载会删除 RelayPilot 与托管代理配置。",
	}
	rows := [][]any{}
	appendTGRow(&rows, tgButtonIfSafe("先预览", "rp:agent:retire-preview:"+mode+":"+agentID))
	appendTGRow(&rows, tgButtonIfSafe("确认执行", "rp:agent:retire-confirm:"+mode+":"+agentID))
	appendTGRow(&rows, tgButtonIfSafe("⬅️ 返回退役方式", "rp:agent:retire:"+agentID))
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgKeyboard(rows...),
		CallbackText: "确认退役",
	}
}

func dispatchHubCommandReply(stateDir, commandText string, cb obj, replyMarkup obj, callbackText string) telegramReply {
	out, err := hubDispatchCommand(stateDir, commandText)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	if batchID := extractBatchID(out); batchID != "" {
		chat := asObj(asObj(cb["message"])["chat"])
		if err := recordPendingTGBatch(stateDir, batchID, commandText, chat["id"]); err != nil {
			out += "\n⚠️ 汇总跟踪失败：" + err.Error()
		}
	}
	if replyMarkup == nil {
		replyMarkup = tgBackKeyboard()
	}
	return telegramReply{Text: out, ReplyMarkup: replyMarkup, CallbackText: callbackText}
}

func agentRetirePreviewReply(stateDir, agentID, mode string, cb obj) telegramReply {
	mode, err := normalizeDecommissionMode(mode)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	return dispatchHubCommandReply(
		stateDir,
		"/decommission "+agentID+" --mode "+mode,
		cb,
		tgKeyboardFromButtons(
			tgButtonIfSafe("确认执行", "rp:agent:retire-confirm:"+mode+":"+agentID),
			tgButtonIfSafe("⬅️ 返回节点", "rp:agent:"+agentID),
		),
		"退役预览",
	)
}

func agentRetireExecuteReply(stateDir, agentID, mode string, cb obj) telegramReply {
	mode, err := normalizeDecommissionMode(mode)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	return dispatchHubCommandReply(
		stateDir,
		"/decommission "+agentID+" --mode "+mode+" --confirm "+agentID,
		cb,
		tgKeyboardFromButtons(tgButtonIfSafe("⬅️ 返回节点", "rp:agent:"+agentID)),
		"已下发退役",
	)
}

func updateCenterReply() telegramReply {
	lines := []string{
		"⬆️ <b>RelayPilot 更新中心</b>",
		"当前 Hub 版本：" + htmlCode(buildVersion),
		"默认：latest + 重启",
		"",
		"流程：选择目标 → 确认执行。",
		"建议：Hub → 单个 Agent → 按角色批量 → 全 Agent。",
	}
	return telegramReply{
		Text:      strings.Join(lines, "\n"),
		ParseMode: "HTML",
		ReplyMarkup: tgKeyboard(
			[]any{tgButton("⬆️ 更新 Hub", "rp:upd:hub"), tgButton("⬆️ 更新 Agent", "rp:upd:agent")},
			[]any{tgButton("⬅️ 返回总控", "rp:panel")},
		),
		CallbackText: "更新中心",
	}
}

func updateAgentPickerReply(stateDir string) telegramReply {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return textPanelReply("❌ " + err.Error())
	}
	lines := []string{
		"⬆️ <b>选择 Agent 更新范围</b>",
		"",
		"默认：latest + 重启 Agent 服务。",
		"建议先选单个节点 canary，再更新角色或全部。",
	}
	selectable := make([]obj, 0, len(agents))
	for _, agent := range agents {
		if str(agent["status"]) == "pending" || str(agent["id"]) == "" {
			continue
		}
		selectable = append(selectable, agent)
	}
	rows := [][]any{}
	if len(selectable) == 0 {
		lines = append(lines, "", "当前没有已接入 Agent。")
	} else {
		rows = append(rows, []any{tgButton("全部 Agent", "rp:upd:agent:all")})
		rows = append(rows, []any{tgButton("Transit", "rp:upd:agent:transit"), tgButton("Landing", "rp:upd:agent:landing")})
		count := 0
		for _, agent := range selectable {
			agentID := str(agent["id"])
			callback := "rp:upd:agent:" + agentID
			if len(callback) > 64 {
				continue
			}
			rows = append(rows, []any{tgButton(roleIcon(str(agent["role"]))+" "+agentID, callback)})
			count++
			if count >= 8 {
				break
			}
		}
	}
	rows = append(rows, []any{tgButton("⬅️ 返回更新中心", "rp:update")})
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgKeyboard(rows...),
		CallbackText: "选择更新范围",
	}
}

func updateTargetLabel(selector string) string {
	switch selector {
	case "hub":
		return "Hub"
	case "all":
		return "全部 Agent"
	case "transit":
		return "Transit"
	case "landing":
		return "Landing"
	default:
		return "Agent：" + selector
	}
}

func updateConfirmReply(selector string) telegramReply {
	return updateConfirmReplyWithBack(selector, "")
}

func updateConfirmReplyWithBack(selector, backOverride string) telegramReply {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return textPanelReply("❌ 缺少更新目标")
	}
	targetLabel := updateTargetLabel(selector)
	back := "rp:upd:agent"
	executeLine := "执行：确认后下发任务，Agent 轮询领取。"
	if selector == "hub" {
		back = "rp:update"
		executeLine = "执行：确认后更新 Hub，并重启 Hub/Bot 服务。"
	}
	if backOverride != "" {
		back = backOverride
	}
	lines := []string{
		"⬆️ <b>确认更新</b>",
		"",
		"目标：" + html.EscapeString(targetLabel),
		"版本：latest",
		"重启：是",
		executeLine,
	}
	return telegramReply{
		Text:      strings.Join(lines, "\n"),
		ParseMode: "HTML",
		ReplyMarkup: tgKeyboard(
			[]any{tgButton("确认更新并重启", "rp:upd:run:"+selector)},
			[]any{tgButton("⬅️ 返回", back)},
		),
		CallbackText: "确认更新",
	}
}

func dispatchHubCommandFromCallback(stateDir, commandText string, cb obj) telegramReply {
	return dispatchHubCommandReply(stateDir, commandText, cb, tgBackKeyboard(), "OK")
}

func updateCallbackReply(stateDir, data string, cb obj) telegramReply {
	switch {
	case data == "rp:upd:hub":
		return updateConfirmReply("hub")
	case data == "rp:upd:agent":
		return updateAgentPickerReply(stateDir)
	case strings.HasPrefix(data, "rp:upd:agent:"):
		selector := strings.TrimPrefix(data, "rp:upd:agent:")
		return updateConfirmReply(selector)
	case strings.HasPrefix(data, "rp:upd:run:"):
		selector := strings.TrimPrefix(data, "rp:upd:run:")
		if selector == "hub" {
			return dispatchHubCommandFromCallback(stateDir, "/relaypilot_uphub", cb)
		}
		return dispatchHubCommandFromCallback(stateDir, "/relaypilot_up "+selector, cb)
	default:
		return textPanelReply("❌ 未识别更新操作")
	}
}

func agentCallbackReply(stateDir, data string, cb obj) telegramReply {
	rest := strings.TrimPrefix(data, "rp:agent:")
	if rest == "" {
		return textPanelReply("❌ 缺少节点 ID")
	}
	parts := strings.Split(rest, ":")
	switch parts[0] {
	case "sync":
		if len(parts) != 2 || parts[1] == "" {
			return textPanelReply("❌ 缺少节点 ID")
		}
		return dispatchHubCommandFromCallback(stateDir, "/sync "+parts[1], cb)
	case "doctor":
		if len(parts) != 2 || parts[1] == "" {
			return textPanelReply("❌ 缺少节点 ID")
		}
		return dispatchHubCommandFromCallback(stateDir, "/doctor "+parts[1], cb)
	case "endpoints":
		if len(parts) != 2 || parts[1] == "" {
			return textPanelReply("❌ 缺少节点 ID")
		}
		return dispatchHubCommandFromCallback(stateDir, "/endpoints "+parts[1], cb)
	case "related":
		if len(parts) != 2 || parts[1] == "" {
			return textPanelReply("❌ 缺少节点 ID")
		}
		return agentRelatedReply(stateDir, parts[1])
	case "update":
		if len(parts) != 2 || parts[1] == "" {
			return textPanelReply("❌ 缺少节点 ID")
		}
		return updateConfirmReplyWithBack(parts[1], "rp:agent:"+parts[1])
	case "results":
		if len(parts) != 2 || parts[1] == "" {
			return textPanelReply("❌ 缺少节点 ID")
		}
		return agentTaskResultsReply(stateDir, parts[1])
	case "retire":
		if len(parts) != 2 || parts[1] == "" {
			return textPanelReply("❌ 缺少节点 ID")
		}
		return agentRetireReply(stateDir, parts[1])
	case "retire-mode":
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return textPanelReply("❌ 缺少退役参数")
		}
		return agentRetireConfirmReply(parts[2], parts[1])
	case "retire-preview":
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return textPanelReply("❌ 缺少退役参数")
		}
		return agentRetirePreviewReply(stateDir, parts[2], parts[1], cb)
	case "retire-confirm":
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return textPanelReply("❌ 缺少退役参数")
		}
		return agentRetireExecuteReply(stateDir, parts[2], parts[1], cb)
	default:
		if strings.Contains(rest, ":") {
			return textPanelReply("❌ 未识别节点操作")
		}
		return agentDetailReply(stateDir, rest)
	}
}

func textPanelReply(text string) telegramReply {
	return telegramReply{Text: text, ReplyMarkup: tgBackKeyboard(), CallbackText: "OK"}
}

func runTelegramHubDaemon(stateDir string, interval, timeout, limit int, quiet bool) error {
	if interval < 1 {
		interval = 1
	}
	if timeout < 1 {
		timeout = 25
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	offset := int64(0)
	backoff := time.Duration(interval) * time.Second
	for {
		cfg, err := loadTelegramConfig(stateDir)
		if err != nil {
			return err
		}
		resp, err := telegramAPIRequest(cfg, "getUpdates", obj{
			"offset":          offset,
			"timeout":         timeout,
			"limit":           limit,
			"allowed_updates": []any{"message", "callback_query"},
		}, time.Duration(timeout)*time.Second)
		if err != nil {
			if !quiet {
				fmt.Fprintf(os.Stderr, "telegram getUpdates error: %v; retry in %s\n", err, backoff)
			}
			time.Sleep(backoff)
			backoff *= 2
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			continue
		}
		backoff = time.Duration(interval) * time.Second
		updates := asList(resp["result"])
		for _, raw := range updates {
			update := asObj(raw)
			updateID := int64Value(update["update_id"])
			if updateID >= offset {
				offset = updateID + 1
			}
			if !telegramUpdateAllowed(cfg, update) {
				if !quiet {
					fmt.Fprintf(os.Stderr, "telegram ignored update from chat %s\n", telegramUpdateChatID(update))
				}
				continue
			}
			reply := handleTelegramHubReply(stateDir, update)
			if reply.Text == "" {
				continue
			}
			if cb := asObj(update["callback_query"]); len(cb) > 0 {
				callbackText := firstNonEmpty(reply.CallbackText, "OK")
				if err := answerTelegramCallback(cfg, str(cb["id"]), callbackText, 5*time.Second); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "telegram answerCallbackQuery error: %v\n", err)
				}
			}
			if err := sendTelegramReplyTo(cfg, cfg["chat_id"], reply, 10*time.Second); err != nil && !quiet {
				fmt.Fprintf(os.Stderr, "telegram sendMessage error: %v\n", err)
			}
		}
		ready, err := collectReadyPendingTGBatches(stateDir)
		if err != nil {
			if !quiet {
				fmt.Fprintf(os.Stderr, "telegram pending batch check error: %v\n", err)
			}
		} else {
			for _, batch := range ready {
				if err := sendTelegramMessageTo(cfg, batch["chat_id"], str(batch["text"]), 10*time.Second); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "telegram batch result send error: %v\n", err)
				}
			}
		}
		if len(updates) == 0 {
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}
}

func operationGuideReply() telegramReply {
	lines := []string{
		"🧭 <b>RelayPilot 操作向导</b>",
		"",
		"1. <b>Hub</b>：生成邀请码，分别接入中转和落地。",
		"2. <b>Agent</b>：按角色完成本机 Reality / Shadowsocks / SOCKS5 配置。",
		"3. <b>Hub</b>：点“刷新节点详情”，更新节点拓扑和公网入口。",
		"4. <b>Hub</b>：串联节点，系统会让落地回传 endpoint，再让中转绑定。",
		"5. <b>检测</b>：按需做一次链路检测；默认 3s 超时，不开后台监控。",
		"6. <b>结果</b>：最近操作里应看到 sing-box check、热重载、运行状态确认。",
		"7. <b>客户端</b>：链路确认后再导出 RemnaWave / 客户端配置。",
		"",
		"提示：公网 IP 模式只做可见性；真正对外服务地址用“公网入口”。",
	}
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgBackKeyboard(),
		CallbackText: "操作向导",
	}
}

func endpointNameForProbeExample(landing, link obj) string {
	if name := str(link["endpoint_name"]); name != "" {
		return name
	}
	outboundTag := str(link["outbound_tag"])
	if outboundTag != "" {
		for _, raw := range asList(asObj(landing["topology"])["endpoints"]) {
			endpoint := asObj(raw)
			if str(endpoint["tag"]) == outboundTag && str(endpoint["name"]) != "" {
				return str(endpoint["name"])
			}
		}
	}
	return firstLandingEndpointName(landing)
}

func probeHelpCommandExamples(stateDir string, limit int) []string {
	if limit <= 0 {
		limit = 5
	}
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return nil
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
	var examples []string
	for _, transit := range transits {
		for _, landing := range landings {
			for _, link := range matchingTransitLinksForLanding(transit, landing) {
				endpointName := endpointNameForProbeExample(landing, link)
				if endpointName == "" {
					continue
				}
				examples = append(examples, fmt.Sprintf("/relaypilot_probe %s %s %s", str(transit["id"]), str(landing["id"]), endpointName))
				if len(examples) >= limit {
					return examples
				}
			}
		}
	}
	return examples
}

func probeHelpReply(stateDir string) telegramReply {
	lines := []string{
		"🧪 <b>链路检测</b>",
		"",
		"只对一条 Transit → Landing 做一次 TCP 连通性探测。",
		"不会启动后台监控，也不会对所有节点并发扫描。",
		"",
		"<b>建议流程：</b>",
		"1. 先点“刷新节点详情”，确保 Hub 有最新 endpoint/链路快照。",
		"2. 从“拓扑”复制 transit_id 和 landing_id。",
		"3. 发送一条检测命令：",
		htmlCode("/relaypilot_probe transit-la landing-jp jp"),
		"",
		"默认超时 3s，最大 5s；如需指定：",
		htmlCode("/relaypilot_check_link transit-la landing-jp jp --timeout 3"),
	}
	if examples := probeHelpCommandExamples(stateDir, 5); len(examples) > 0 {
		lines = append(lines,
			"",
			"<b>可检测链路：</b>",
			"仅生成命令，不会立即探测；复制其中一条发送即可。",
		)
		for _, example := range examples {
			lines = append(lines, htmlCode(example))
		}
	}
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgBackKeyboard(),
		CallbackText: "链路检测",
	}
}

func handleTelegramHubUpdate(stateDir string, update obj) string {
	return handleTelegramHubReply(stateDir, update).Text
}

func handleTelegramHubReply(stateDir string, update obj) telegramReply {
	if cb := asObj(update["callback_query"]); len(cb) > 0 {
		data := resolveTelegramCallbackData(stateDir, str(cb["data"]))
		if data == "" {
			return telegramReply{}
		}
		switch data {
		case "rp:panel":
			return hubPanelReply(stateDir)
		case "rp:guide":
			return operationGuideReply()
		case "rp:probe_help":
			return probeHelpReply(stateDir)
		case "rp:sync_all":
			out, err := hubDispatchCommand(stateDir, "/sync all")
			if err != nil {
				return textPanelReply("❌ " + err.Error())
			}
			return textPanelReply(out)
		case "rp:update":
			return updateCenterReply()
		case "rp:topology":
			out, err := hubDispatchCommand(stateDir, "/topology")
			if err != nil {
				return textPanelReply("❌ " + err.Error())
			}
			return telegramReply{
				Text: out,
				ReplyMarkup: tgKeyboard(
					[]any{tgButton("🧪 链路检测", "rp:probe_help")},
					[]any{tgButton("⬅️ 返回总控", "rp:panel")},
				),
				CallbackText: "拓扑",
			}
		case "rp:agents":
			return agentListReply(stateDir)
		case "rp:results":
			out, err := hubDispatchCommand(stateDir, "/results")
			if err != nil {
				return textPanelReply("❌ " + err.Error())
			}
			return textPanelReply(out)
		}
		if strings.HasPrefix(data, "rp:upd:") {
			return updateCallbackReply(stateDir, data, cb)
		}
		if strings.HasPrefix(data, "rp:agent:") {
			return agentCallbackReply(stateDir, data, cb)
		}
		result, err := handleHubAlertCallback(stateDir, data, offlineAlertSnoozeSecs)
		if err != nil {
			return textPanelReply("❌ " + err.Error())
		}
		return textPanelReply(str(result["message"]))
	}
	message := asObj(update["message"])
	if len(message) == 0 {
		return telegramReply{}
	}
	text := strings.TrimSpace(str(message["text"]))
	if text == "" || !strings.HasPrefix(text, "/") {
		return telegramReply{}
	}
	rawCommand := strings.Fields(text)[0]
	if !isRelayPilotTelegramCommand(rawCommand) {
		return telegramReply{}
	}
	command := normalizeTelegramCommand(rawCommand)
	if command == "relaypilot" || command == "panel" {
		return hubPanelReply(stateDir)
	}
	out, err := hubDispatchCommand(stateDir, text)
	if err != nil {
		return telegramReply{Text: "❌ " + err.Error()}
	}
	if batchID := extractBatchID(out); batchID != "" {
		chat := asObj(message["chat"])
		if err := recordPendingTGBatch(stateDir, batchID, text, chat["id"]); err != nil {
			return telegramReply{Text: out + "\n⚠️ 汇总跟踪失败：" + err.Error()}
		}
	}
	return telegramReply{Text: out}
}
