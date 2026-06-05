package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func defaultHubRegistry() obj {
	return obj{"kind": "relaypilot/hub-registry", "version": version, "agents": obj{}}
}

type hubTaskSummary struct {
	Queued          int
	RecentLine      string
	Total           int
	Done            int
	Failed          int
	Running         int
	Cancelled       int
	CompletedRecent int
}

type hubTaskCacheEntry struct {
	signature string
	tasks     []obj
	summary   hubTaskSummary
	cachedAt  time.Time
}

type hubAgentCacheEntry struct {
	signature string
	agents    []obj
	cachedAt  time.Time
}

type hubTopologyIndex struct {
	signature    string
	topologyText string
	related      map[string][]relatedAgent
	cachedAt     time.Time
}

const (
	hubAgentCacheMaxAge    = 10 * time.Second
	hubTopologyCacheMaxAge = 10 * time.Second
	hubTaskCacheMaxAge     = 5 * time.Second
)

var hubViewCache = struct {
	sync.Mutex
	agents   map[string]hubAgentCacheEntry
	tasks    map[string]hubTaskCacheEntry
	topology map[string]hubTopologyIndex
}{
	agents:   map[string]hubAgentCacheEntry{},
	tasks:    map[string]hubTaskCacheEntry{},
	topology: map[string]hubTopologyIndex{},
}

func hubAgentsFileSignature(stateDir string) (string, error) {
	info, err := os.Stat(hubAgentsPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano()), nil
}

func hubTasksDirSignature(stateDir string) (string, error) {
	dir := hubTasksDir(stateDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s:%d:%d\n", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func invalidateHubAgentCache(stateDir string) {
	hubViewCache.Lock()
	defer hubViewCache.Unlock()
	delete(hubViewCache.agents, stateDir)
	delete(hubViewCache.topology, stateDir)
}

func invalidateHubTaskCache(stateDir string) {
	hubViewCache.Lock()
	defer hubViewCache.Unlock()
	delete(hubViewCache.tasks, stateDir)
}

func hubCacheFresh(cachedAt time.Time, maxAge time.Duration) bool {
	return !cachedAt.IsZero() && time.Since(cachedAt) < maxAge
}

func loadHubRegistry(stateDir string) (obj, error) {
	path := hubAgentsPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return defaultHubRegistry(), nil
	}
	reg, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if _, ok := reg["agents"].(map[string]any); !ok {
		reg["agents"] = obj{}
	}
	return reg, nil
}

func saveHubRegistry(stateDir string, reg obj) error {
	if err := writeJSON(hubAgentsPath(stateDir), reg, 0o600); err != nil {
		return err
	}
	invalidateHubAgentCache(stateDir)
	return nil
}

func defaultRemovedAgents() obj {
	return obj{"kind": "relaypilot/hub-removed-agents", "version": version, "agents": obj{}}
}

func loadRemovedAgents(stateDir string) (obj, error) {
	path := hubRemovedAgentsPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return defaultRemovedAgents(), nil
	}
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if _, ok := data["agents"].(map[string]any); !ok {
		data["agents"] = obj{}
	}
	if str(data["kind"]) == "" {
		data["kind"] = "relaypilot/hub-removed-agents"
	}
	if int64Value(data["version"]) == 0 {
		data["version"] = version
	}
	return data, nil
}

func saveRemovedAgents(stateDir string, data obj) error {
	return writeJSON(hubRemovedAgentsPath(stateDir), data, 0o600)
}

func defaultHubAlerts() obj {
	return obj{"kind": "relaypilot/hub-alerts", "version": version, "alerts": obj{}}
}

func loadHubAlertsData(stateDir string) (obj, error) {
	path := hubAlertsPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return defaultHubAlerts(), nil
	}
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if _, ok := data["alerts"].(map[string]any); !ok {
		data["alerts"] = obj{}
	}
	if str(data["kind"]) == "" {
		data["kind"] = "relaypilot/hub-alerts"
	}
	if int64Value(data["version"]) == 0 {
		data["version"] = version
	}
	return data, nil
}

func saveHubAlertsData(stateDir string, data obj) error {
	return writeJSON(hubAlertsPath(stateDir), data, 0o600)
}

func validateRegistration(in obj) (obj, error) {
	if str(in["kind"]) != agentRegistrationKind {
		return nil, fmt.Errorf("unsupported agent registration kind: %q", in["kind"])
	}
	if int64Value(in["version"]) != version {
		return nil, fmt.Errorf("unsupported agent registration version: %v", in["version"])
	}
	agentID, err := ensureSafeName(str(in["id"]), "agent.id")
	if err != nil {
		return nil, err
	}
	role := str(in["role"])
	if role != "transit" && role != "landing" && role != "hub" {
		return nil, fmt.Errorf("agent.role must be transit, landing, or hub")
	}
	out := obj{}
	for k, v := range in {
		out[k] = v
	}
	out["id"] = agentID
	out["role"] = role
	if str(out["name"]) == "" {
		out["name"] = agentID
	}
	if _, ok := out["labels"].(map[string]any); !ok {
		out["labels"] = obj{}
	}
	if _, ok := out["capabilities"].([]any); !ok {
		out["capabilities"] = []any{"status", "doctor", "endpoints", "inspect_conf"}
	}
	if str(out["transport"]) == "" {
		out["transport"] = "poll"
	}
	topo := asObj(out["topology"])
	if _, ok := topo["links"].([]any); !ok {
		topo["links"] = []any{}
	}
	if _, ok := topo["endpoints"].([]any); !ok {
		topo["endpoints"] = []any{}
	}
	if _, ok := topo["inbounds"].([]any); !ok {
		topo["inbounds"] = []any{}
	}
	out["topology"] = topo
	health := asObj(out["health"])
	if str(health["status"]) == "" {
		health["status"] = "unknown"
	}
	out["health"] = health
	n := now()
	if int64Value(out["registered_at"]) == 0 {
		out["registered_at"] = n
	}
	if int64Value(out["updated_at"]) == 0 {
		out["updated_at"] = n
	}
	if int64Value(out["last_seen"]) == 0 {
		out["last_seen"] = out["updated_at"]
	}
	return out, nil
}

func parseLabels(value string) (obj, error) {
	labels := obj{}
	value = strings.TrimSpace(value)
	if value == "" {
		return labels, nil
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, val, ok := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("label must be key=value: %q", item)
		}
		labels[key] = strings.TrimSpace(val)
	}
	return labels, nil
}

func makeAgentRegistration(agentID, role, name, endpoint, labelsValue, transport, stateDir, conf string, noSnapshot bool) (obj, error) {
	labels, err := parseLabels(labelsValue)
	if err != nil {
		return nil, err
	}
	topology := obj{}
	if !noSnapshot {
		topology = collectTopology(role, stateDir, conf)
	}
	health := healthFromTopology(topology)
	n := now()
	registration := obj{
		"kind":         agentRegistrationKind,
		"version":      version,
		"id":           agentID,
		"role":         role,
		"name":         firstNonEmpty(name, agentID),
		"transport":    firstNonEmpty(transport, "poll"),
		"labels":       labels,
		"capabilities": []any{"status", "doctor", "endpoints", "inspect_conf"},
		"topology":     topology,
		"health":       health,
		"last_seen":    n,
		"updated_at":   n,
	}
	if endpoint != "" {
		registration["endpoint"] = endpoint
	}
	return validateRegistration(registration)
}

func importAgentRegistration(stateDir, path string) (obj, error) {
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	agent, err := validateRegistration(data)
	if err != nil {
		return nil, err
	}
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	agents := asObj(reg["agents"])
	agents[str(agent["id"])] = agent
	reg["agents"] = agents
	return agent, saveHubRegistry(stateDir, reg)
}

func listHubAgents(stateDir string) ([]obj, error) {
	signature, err := hubAgentsFileSignature(stateDir)
	if err != nil {
		return nil, err
	}
	hubViewCache.Lock()
	if cached, ok := hubViewCache.agents[stateDir]; ok && cached.signature == signature && hubCacheFresh(cached.cachedAt, hubAgentCacheMaxAge) {
		agents := cloneObjListDeep(cached.agents)
		hubViewCache.Unlock()
		return agents, nil
	}
	hubViewCache.Unlock()
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	agentsMap := asObj(reg["agents"])
	out := make([]obj, 0, len(agentsMap))
	for _, v := range agentsMap {
		if m := asObj(v); len(m) > 0 {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return str(out[i]["role"])+str(out[i]["id"]) < str(out[j]["role"])+str(out[j]["id"])
	})
	hubViewCache.Lock()
	hubViewCache.agents[stateDir] = hubAgentCacheEntry{signature: signature, agents: cloneObjListDeep(out), cachedAt: time.Now()}
	hubViewCache.Unlock()
	return cloneObjListDeep(out), nil
}

func listRemovedAgents(stateDir string) ([]obj, error) {
	data, err := loadRemovedAgents(stateDir)
	if err != nil {
		return nil, err
	}
	agentsMap := asObj(data["agents"])
	out := make([]obj, 0, len(agentsMap))
	for _, v := range agentsMap {
		if m := asObj(v); len(m) > 0 {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return int64Value(out[i]["removed_at"]) > int64Value(out[j]["removed_at"]) })
	return out, nil
}

func summarizeAgent(agent obj) obj {
	out := obj{
		"id":           agent["id"],
		"role":         agent["role"],
		"name":         agent["name"],
		"transport":    agent["transport"],
		"endpoint":     agent["endpoint"],
		"labels":       asObj(agent["labels"]),
		"capabilities": asList(agent["capabilities"]),
		"health":       asObj(agent["health"]),
		"topology":     asObj(agent["topology"]),
		"last_seen":    agent["last_seen"],
		"updated_at":   agent["updated_at"],
	}
	return out
}

func findHubAgentByID(agents []obj, agentID string) (obj, error) {
	for _, agent := range agents {
		if str(agent["id"]) == agentID {
			return agent, nil
		}
	}
	return nil, fmt.Errorf("agent not found: %s", agentID)
}

func findPublicEntryForAgent(agent obj, use, endpointName string) obj {
	entries := asList(asObj(agent["network"])["public_entries"])
	for _, raw := range entries {
		entry := asObj(raw)
		if str(entry["use"]) != use {
			continue
		}
		if endpointName == "" || str(entry["name"]) == endpointName {
			return entry
		}
	}
	return nil
}

func firstTransitRealityInbound(agent obj) obj {
	for _, raw := range asList(asObj(agent["topology"])["inbounds"]) {
		inbound := asObj(raw)
		if str(inbound["type"]) == "vless" {
			return inbound
		}
	}
	return nil
}

func transitRealityClientMaterial(agent obj, authUser string) (obj, error) {
	reality := asObj(asObj(agent["topology"])["reality_client"])
	if len(reality) == 0 {
		return nil, errors.New("transit reality client material is unavailable; agent must report reality client fields")
	}
	inbound := firstTransitRealityInbound(agent)
	if len(inbound) == 0 {
		return nil, errors.New("transit inbound snapshot is unavailable")
	}
	var selectedUser obj
	users := asList(inbound["users"])
	if authUser != "" {
		for _, raw := range users {
			user := asObj(raw)
			if str(user["name"]) == authUser {
				selectedUser = user
				break
			}
		}
		if len(selectedUser) == 0 {
			return nil, fmt.Errorf("auth_user not found on transit inbound: %s", authUser)
		}
	} else if len(users) == 1 {
		selectedUser = asObj(users[0])
		authUser = str(selectedUser["name"])
	} else {
		return nil, errors.New("multiple users found; --auth-user is required")
	}
	entry := findPublicEntryForAgent(agent, "reality", "")
	host := str(entry["host"])
	port := int64Value(entry["public_port"])
	if host == "" || port <= 0 {
		if host = str(asObj(agent["network"])["public_ip"]); host == "" {
			host = str(asObj(agent["network"])["observed_ip"])
		}
		port = int64Value(inbound["listen_port"])
	}
	if host == "" || port <= 0 {
		return nil, errors.New("transit public host/port is unavailable; set a reality public entry or public IP")
	}
	flow := firstNonEmpty(str(selectedUser["flow"]), "xtls-rprx-vision")
	name := firstNonEmpty(str(selectedUser["name"]), authUser)
	exported := obj{
		"kind":        "relaypilot/client-export",
		"version":     version,
		"format":      "remnawave",
		"agent_id":    agent["id"],
		"agent_name":  agent["name"],
		"auth_user":   name,
		"server":      host,
		"server_port": port,
		"uuid":        selectedUser["uuid"],
		"flow":        flow,
		"transport":   "tcp",
		"security":    "reality",
		"server_name": reality["server_name"],
		"public_key":  reality["public_key"],
		"short_id":    reality["short_id"],
		"fingerprint": "chrome",
		"profile": obj{
			"type":         "vless",
			"tag":          name,
			"server":       host,
			"server_port":  port,
			"uuid":         selectedUser["uuid"],
			"network":      "tcp",
			"tls":          true,
			"flow":         flow,
			"security":     "reality",
			"server_name":  reality["server_name"],
			"public_key":   reality["public_key"],
			"short_id":     reality["short_id"],
			"fingerprint":  "chrome",
			"inbound_tag":  reality["inbound_tag"],
			"transit_name": agent["name"],
		},
	}
	return exported, nil
}

func hubExportTransitClient(stateDir, transitID, authUser string) (obj, error) {
	if _, err := ensureSafeName(transitID, "transit.id"); err != nil {
		return nil, err
	}
	if authUser != "" {
		if _, err := ensureSafeName(authUser, "auth_user"); err != nil {
			return nil, err
		}
	}
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return nil, err
	}
	transit, err := findHubAgentByID(agents, transitID)
	if err != nil {
		return nil, err
	}
	if str(transit["role"]) != "transit" {
		return nil, fmt.Errorf("%s is not a transit agent", transitID)
	}
	exported, err := transitRealityClientMaterial(transit, authUser)
	if err != nil {
		return nil, err
	}
	syncAt := int64Value(transit["sync_at"])
	exported["cache"] = obj{
		"source":    "hub-registry",
		"synced_at": syncAt,
		"stale":     syncAt <= 0 || now()-syncAt > defaultSyncStaleSeconds,
	}
	return exported, nil
}

func hubExportLandingCachePath(stateDir, landingID, endpointName string) (string, error) {
	if _, err := ensureSafeName(landingID, "landing.id"); err != nil {
		return "", err
	}
	if _, err := ensureSafeName(endpointName, "endpoint.name"); err != nil {
		return "", err
	}
	return filepath.Join(hubExportsDir(stateDir), "landing-"+landingID+"-"+endpointName+".json"), nil
}

func storeHubLandingExport(stateDir, landingID string, endpoint obj, publicEntries obj) error {
	endpointName := str(endpoint["name"])
	path, err := hubExportLandingCachePath(stateDir, landingID, endpointName)
	if err != nil {
		return err
	}
	record := obj{
		"kind":          "relaypilot/hub-landing-export",
		"version":       version,
		"landing_id":    landingID,
		"endpoint_name": endpointName,
		"stored_at":     now(),
		"endpoint":      endpoint,
	}
	if len(publicEntries) > 0 {
		record["public_entries"] = publicEntries
	}
	return writeJSON(path, record, 0o600)
}

func loadHubLandingExport(stateDir, landingID, endpointName string) (obj, error) {
	path, err := hubExportLandingCachePath(stateDir, landingID, endpointName)
	if err != nil {
		return nil, err
	}
	return loadJSON(path)
}

func hubExportLandingConfig(stateDir, landingID, endpointName string) (obj, error) {
	if _, err := ensureSafeName(landingID, "landing.id"); err != nil {
		return nil, err
	}
	if endpointName == "" {
		return nil, errors.New("--endpoint-name is required")
	}
	if _, err := ensureSafeName(endpointName, "endpoint.name"); err != nil {
		return nil, err
	}
	record, err := loadHubLandingExport(stateDir, landingID, endpointName)
	if err != nil {
		return nil, err
	}
	endpoint, err := validateEndpoint(asObj(record["endpoint"]), true)
	if err != nil {
		return nil, err
	}
	exported := obj{
		"kind":          "relaypilot/landing-client-export",
		"version":       version,
		"landing_id":    landingID,
		"endpoint_name": endpointName,
		"protocol":      endpoint["protocol"],
		"server":        endpoint["server"],
		"server_port":   endpoint["server_port"],
		"stored_at":     record["stored_at"],
	}
	switch str(endpoint["protocol"]) {
	case "socks":
		exported["config"] = obj{
			"type":        "socks",
			"server":      endpoint["server"],
			"server_port": endpoint["server_port"],
			"version":     firstNonNil(endpoint["socks_version"], endpoint["version_field"], "5"),
			"username":    endpoint["username"],
			"password":    endpoint["password"],
		}
	case "shadowsocks":
		exported["config"] = obj{
			"type":        "shadowsocks",
			"server":      endpoint["server"],
			"server_port": endpoint["server_port"],
			"method":      endpoint["method"],
			"password":    endpoint["password"],
			"network":     endpoint["network"],
		}
	default:
		return nil, fmt.Errorf("unsupported landing export protocol: %s", endpoint["protocol"])
	}
	storedAt := int64Value(record["stored_at"])
	exported["cache"] = obj{
		"source":    "hub-export-file",
		"synced_at": storedAt,
		"stale":     storedAt <= 0 || now()-storedAt > defaultSyncStaleSeconds,
	}
	return exported, nil
}

func storeHubSyncSnapshot(stateDir, agentID string, result obj) error {
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return err
	}
	agents := asObj(reg["agents"])
	current := asObj(agents[agentID])
	if len(current) == 0 {
		return fmt.Errorf("agent not found: %s", agentID)
	}
	if topology := asObj(result["topology"]); len(topology) > 0 {
		current["topology"] = topology
	}
	if network := asObj(result["network"]); len(network) > 0 {
		current["network"] = network
	}
	current["sync_at"] = now()
	agents[agentID] = current
	reg["agents"] = agents
	return saveHubRegistry(stateDir, reg)
}

func hubSyncAgent(stateDir, agentID string) (obj, error) {
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return nil, err
	}
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return nil, err
	}
	agent, err := findHubAgentByID(agents, agentID)
	if err != nil {
		return nil, err
	}
	task, err := createHubTask(stateDir, agent, "sync_agent", nil, fmt.Sprintf("%d-%s", now(), mustRandomHex(4)), "hub-sync-agent")
	if err != nil {
		return nil, err
	}
	return obj{"queued": task["id"], "agent_id": agentID, "command": "sync_agent"}, nil
}

func hubSyncAll(stateDir string) (obj, error) {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return nil, err
	}
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	queued := []any{}
	for _, agent := range agents {
		task, err := createHubTask(stateDir, agent, "sync_agent", nil, batchID, "hub-sync-all")
		if err != nil {
			return nil, err
		}
		queued = append(queued, obj{"task_id": task["id"], "agent_id": agent["id"]})
	}
	return obj{"batch_id": batchID, "queued": queued}, nil
}

func queueHubDetailRefreshForAgents(stateDir string, agents []obj, originText, reason string) (obj, error) {
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	queued := []any{}
	seen := map[string]bool{}
	for _, agent := range agents {
		agentID := str(agent["id"])
		if agentID == "" || seen[agentID] || isPendingAgent(agent) {
			continue
		}
		seen[agentID] = true
		task, err := createHubTask(stateDir, agent, "sync_agent", nil, batchID, originText)
		if err != nil {
			return nil, err
		}
		queued = append(queued, obj{"task_id": task["id"], "agent_id": agentID})
	}
	if len(queued) == 0 {
		return nil, errors.New("no active agents available for detail refresh")
	}
	return obj{"batch_id": batchID, "queued": queued, "reason": reason}, nil
}

func hashToken(token string) (string, error) {
	if token == "" {
		return "", errors.New("agent token is required")
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]), nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "="), nil
}

func loadTokenStore(stateDir string) (obj, error) {
	path := hubAgentTokensPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return obj{"kind": "relaypilot/hub-agent-tokens", "version": version, "agents": obj{}}, nil
	}
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if _, ok := data["agents"].(map[string]any); !ok {
		data["agents"] = obj{}
	}
	return data, nil
}

func issueHubAgentToken(stateDir, agentID, token string) (obj, error) {
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return nil, err
	}
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return nil, err
	}
	found := false
	for _, agent := range agents {
		if str(agent["id"]) == agentID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("agent must be imported before issuing token: %s", agentID)
	}
	if token == "" {
		token, err = randomToken(32)
		if err != nil {
			return nil, err
		}
	}
	hash, err := hashToken(token)
	if err != nil {
		return nil, err
	}
	store, err := loadTokenStore(stateDir)
	if err != nil {
		return nil, err
	}
	agentsObj := asObj(store["agents"])
	agentsObj[agentID] = obj{"agent_id": agentID, "token_sha256": hash, "issued_at": now()}
	store["agents"] = agentsObj
	path := hubAgentTokensPath(stateDir)
	if err := writeJSON(path, store, 0o600); err != nil {
		return nil, err
	}
	return obj{"agent_id": agentID, "token": token, "token_file": path, "issued_at": now()}, nil
}

func listHubAgentTokens(stateDir string) ([]obj, error) {
	store, err := loadTokenStore(stateDir)
	if err != nil {
		return nil, err
	}
	agents := asObj(store["agents"])
	ids := make([]string, 0, len(agents))
	for id := range agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]obj, 0, len(ids))
	for _, id := range ids {
		entry := asObj(agents[id])
		out = append(out, obj{
			"agent_id":  firstNonEmpty(str(entry["agent_id"]), id),
			"issued_at": entry["issued_at"],
			"has_token": str(entry["token_sha256"]) != "",
		})
	}
	return out, nil
}

func formatHubAgentTokensText(tokens []obj) string {
	if len(tokens) == 0 {
		return "(no agent tokens)"
	}
	var b strings.Builder
	for _, token := range tokens {
		fmt.Fprintf(&b, "%s\tissued_at=%v\thas_token=%v\n", str(token["agent_id"]), token["issued_at"], token["has_token"])
	}
	return strings.TrimRight(b.String(), "\n")
}

func revokeHubAgentToken(stateDir, agentID string) (obj, error) {
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return nil, err
	}
	store, err := loadTokenStore(stateDir)
	if err != nil {
		return nil, err
	}
	agents := asObj(store["agents"])
	_, revoked := agents[agentID]
	delete(agents, agentID)
	store["agents"] = agents
	path := hubAgentTokensPath(stateDir)
	if err := writeJSON(path, store, 0o600); err != nil {
		return nil, err
	}
	return obj{"agent_id": agentID, "revoked": revoked, "token_file": path, "revoked_at": now()}, nil
}

func rotateHubAgentToken(stateDir, agentID, token string) (obj, error) {
	res, err := issueHubAgentToken(stateDir, agentID, token)
	if err != nil {
		return nil, err
	}
	res["rotated"] = true
	return res, nil
}

func hubAgentAuthKey(stateDir, agentID string) (string, error) {
	store, err := loadTokenStore(stateDir)
	if err != nil {
		return "", err
	}
	entry := asObj(asObj(store["agents"])[agentID])
	key := str(entry["token_sha256"])
	if key == "" {
		return "", fmt.Errorf("agent token not issued: %s", agentID)
	}
	return key, nil
}

func canonicalAuthMessage(method, pathWithQuery string, body []byte, timestamp, nonce, agentID string) string {
	sum := sha256.Sum256(body)
	if pathWithQuery == "" {
		pathWithQuery = "/"
	}
	return strings.Join([]string{strings.ToUpper(method), pathWithQuery, hex.EncodeToString(sum[:]), timestamp, nonce, agentID}, "\n")
}

func signAgentRequest(token, method, pathWithQuery string, body []byte, timestamp, nonce, agentID string) (string, error) {
	key, err := hashToken(token)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(canonicalAuthMessage(method, pathWithQuery, body, timestamp, nonce, agentID)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func loadNonceStore(stateDir string) (obj, error) {
	path := hubAuthNoncesPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return obj{"kind": "relaypilot/hub-auth-nonces", "version": version, "agents": obj{}}, nil
	}
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if _, ok := data["agents"].(map[string]any); !ok {
		data["agents"] = obj{}
	}
	return data, nil
}

func consumeNonce(stateDir, agentID, nonce string, ts int64) error {
	if nonce == "" || len(nonce) > 128 {
		return errors.New("invalid auth nonce")
	}
	store, err := loadNonceStore(stateDir)
	if err != nil {
		return err
	}
	agents := asObj(store["agents"])
	nonces := asObj(agents[agentID])
	floor := now() - agentAuthSkewSeconds
	for k, v := range nonces {
		if int64Value(v) < floor {
			delete(nonces, k)
		}
	}
	if _, exists := nonces[nonce]; exists {
		return errors.New("replayed auth nonce")
	}
	nonces[nonce] = ts
	agents[agentID] = nonces
	store["agents"] = agents
	return writeJSON(hubAuthNoncesPath(stateDir), store, 0o600)
}

func verifySignature(stateDir, agentID, method, pathWithQuery string, body []byte, timestamp, nonce, signature string) bool {
	if agentID == "" || timestamp == "" || nonce == "" || signature == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || abs64(now()-ts) > agentAuthSkewSeconds {
		return false
	}
	key, err := hubAgentAuthKey(stateDir, agentID)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(canonicalAuthMessage(method, pathWithQuery, body, timestamp, nonce, agentID)))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return false
	}
	return consumeNonce(stateDir, agentID, nonce, ts) == nil
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func listEndpoints(stateDir string) ([]obj, error) {
	dir := filepath.Join(stateDir, "endpoints")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []obj{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []obj
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		ep, err := loadJSON(path)
		if err != nil {
			return nil, err
		}
		if str(ep["kind"]) != endpointKind {
			continue
		}
		out = append(out, obj{"name": ep["name"], "protocol": ep["protocol"], "tag": ep["tag"], "server": ep["server"], "server_port": ep["server_port"], "network": ep["network"], "path": path})
	}
	sort.Slice(out, func(i, j int) bool { return str(out[i]["name"]) < str(out[j]["name"]) })
	return out, nil
}

func inspectConfig(conf string) (obj, error) {
	info, err := os.Stat(conf)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var files []string
	if errors.Is(err, os.ErrNotExist) {
		if strings.HasSuffix(conf, ".json") {
			files = []string{}
		} else {
			entries, readErr := os.ReadDir(conf)
			if readErr != nil {
				return nil, readErr
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
					files = append(files, filepath.Join(conf, e.Name()))
				}
			}
		}
	} else if info.IsDir() {
		entries, err := os.ReadDir(conf)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				files = append(files, filepath.Join(conf, e.Name()))
			}
		}
		sort.Strings(files)
		if len(files) > maxConfigFiles {
			files = files[:maxConfigFiles]
		}
	} else {
		files = []string{conf}
	}
	if len(files) == 0 {
		return obj{"conf": conf, "files": files, "inbounds": []any{}, "outbounds": []any{}, "route": obj{"rules": 0, "final": nil, "auth_routes": []any{}}}, nil
	}
	var inbounds, outbounds, authRoutes []any
	routeRules := 0
	var routeFinal any
	for _, file := range files {
		data, err := loadJSON(file)
		if err != nil {
			return nil, err
		}
		for _, raw := range asList(data["inbounds"]) {
			m := asObj(raw)
			var users []any
			for _, u := range asList(m["users"]) {
				um := asObj(u)
				users = append(users, obj{"name": um["name"], "uuid": um["uuid"], "flow": um["flow"]})
			}
			inbounds = append(inbounds, obj{"file": file, "tag": m["tag"], "type": m["type"], "listen": m["listen"], "listen_port": firstNonNil(m["listen_port"], m["port"]), "users": users})
		}
		for _, raw := range asList(data["outbounds"]) {
			m := asObj(raw)
			outbounds = append(outbounds, obj{"file": file, "tag": m["tag"], "type": m["type"], "server": m["server"], "server_port": m["server_port"]})
		}
		route := asObj(data["route"])
		if len(route) > 0 {
			rules := asList(route["rules"])
			routeRules += len(rules)
			if route["final"] != nil {
				routeFinal = route["final"]
			}
			for _, raw := range rules {
				m := asObj(raw)
				if m["auth_user"] != nil && m["outbound"] != nil {
					authRoutes = append(authRoutes, obj{"file": file, "inbound": m["inbound"], "auth_user": m["auth_user"], "outbound": m["outbound"], "action": m["action"]})
				}
			}
		}
	}
	return obj{"conf": conf, "files": files, "inbounds": inbounds, "outbounds": outbounds, "route": obj{"rules": routeRules, "final": routeFinal, "auth_routes": authRoutes}}, nil
}

func firstNonNil(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func formatConfigText(summary obj) string {
	var b strings.Builder
	fmt.Fprintf(&b, "config: %s\n", str(summary["conf"]))
	b.WriteString("inbounds:\n")
	for _, raw := range asList(summary["inbounds"]) {
		m := asObj(raw)
		fmt.Fprintf(&b, "- %s (%s) users=%d\n", str(m["tag"]), str(m["type"]), len(asList(m["users"])))
	}
	b.WriteString("outbounds:\n")
	for _, raw := range asList(summary["outbounds"]) {
		m := asObj(raw)
		target := ""
		if str(m["server"]) != "" {
			target = fmt.Sprintf(" %s:%v", str(m["server"]), m["server_port"])
		}
		fmt.Fprintf(&b, "- %s (%s)%s\n", str(m["tag"]), str(m["type"]), target)
	}
	route := asObj(summary["route"])
	fmt.Fprintf(&b, "route: final=%v rules=%v\n", route["final"], route["rules"])
	for _, raw := range asList(route["auth_routes"]) {
		m := asObj(raw)
		fmt.Fprintf(&b, "- auth_user=%v -> %v\n", m["auth_user"], m["outbound"])
	}
	return b.String()
}

func vlessInboundClientInfo(conf, inboundTag string) (obj, error) {
	files, err := loadConfigFiles(conf)
	if err != nil {
		return nil, err
	}
	_, inbound, err := findVlessInbound(files, inboundTag)
	if err != nil {
		return nil, err
	}
	tlsCfg := asObj(inbound["tls"])
	reality := asObj(tlsCfg["reality"])
	privateKey := str(reality["private_key"])
	publicKey := ""
	if privateKey != "" {
		if _, derived, err := validateRealityPrivateKey(privateKey); err == nil {
			publicKey = derived
		}
	}
	return obj{
		"inbound_tag": str(firstNonNil(inbound["tag"], inboundTag)),
		"listen":      inbound["listen"],
		"listen_port": firstNonNil(inbound["listen_port"], inbound["port"]),
		"server_name": tlsCfg["server_name"],
		"public_key":  publicKey,
		"short_id":    getRealityShortID(inbound),
	}, nil
}

func collectTopology(role, stateDir, conf string) obj {
	topo := obj{"captured_at": now(), "links": []any{}, "endpoints": []any{}, "inbounds": []any{}}
	var errs []any
	endpoints, err := listEndpoints(stateDir)
	if err != nil {
		errs = append(errs, "endpoints: "+err.Error())
	} else {
		epList := make([]any, len(endpoints))
		byTag := map[string]obj{}
		for i, ep := range endpoints {
			epList[i] = ep
			byTag[str(ep["tag"])] = ep
		}
		topo["endpoints"] = epList
		if conf == "" {
			// The export command can intentionally omit --conf: endpoint state is
			// still captured, and config inspection is simply skipped.
		} else if summary, err := inspectConfig(conf); err == nil {
			var inbounds []any
			for _, raw := range asList(summary["inbounds"]) {
				m := asObj(raw)
				inbounds = append(inbounds, obj{"tag": m["tag"], "type": m["type"], "listen": m["listen"], "listen_port": m["listen_port"], "users": m["users"]})
			}
			topo["inbounds"] = inbounds
			if role == "transit" {
				for _, raw := range inbounds {
					inbound := asObj(raw)
					if str(inbound["type"]) == "vless" {
						if clientInfo, err := vlessInboundClientInfo(conf, str(inbound["tag"])); err == nil {
							topo["reality_client"] = clientInfo
						} else {
							errs = append(errs, "reality_client: "+err.Error())
						}
						break
					}
				}
			}
			if role == "transit" {
				var links []any
				for _, raw := range asList(asObj(summary["route"])["auth_routes"]) {
					r := asObj(raw)
					outTag := str(r["outbound"])
					ep := byTag[outTag]
					for _, user := range normalizeList(r["auth_user"]) {
						link := obj{"auth_user": user, "outbound_tag": outTag}
						if ep != nil {
							link["endpoint_name"] = ep["name"]
							link["server"] = ep["server"]
							link["server_port"] = ep["server_port"]
							if mode := str(ep["link_mode"]); mode != "" {
								link["link_mode"] = mode
							}
							if mesh := asObj(ep["mesh"]); len(mesh) > 0 {
								link["mesh"] = mesh
							}
						}
						links = append(links, link)
					}
				}
				topo["links"] = links
			}
		} else {
			errs = append(errs, "conf: "+err.Error())
		}
	}
	if len(errs) > 0 {
		topo["errors"] = errs
	}
	return topo
}

func normalizeList(v any) []any {
	if xs := asList(v); xs != nil {
		return xs
	}
	if v == nil {
		return nil
	}
	return []any{v}
}

func healthFromTopology(topo obj) obj {
	status := "ok"
	if len(asList(topo["errors"])) > 0 {
		status = "degraded"
	}
	return obj{"status": status, "checked_at": now(), "errors": topo["errors"]}
}

func updateHeartbeat(stateDir, agentID string, topology, health, network obj) (obj, error) {
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return nil, err
	}
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	agents := asObj(reg["agents"])
	agent := asObj(agents[agentID])
	if len(agent) == 0 {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	agent["last_seen"] = now()
	agent["updated_at"] = now()
	if topology != nil {
		agent["topology"] = topology
	}
	if health != nil {
		agent["health"] = health
	}
	if network != nil {
		current := asObj(agent["network"])
		if len(current) == 0 {
			current = obj{}
		}
		for k, v := range network {
			current[k] = v
		}
		maybeEnrichNetworkLocation(current)
		agent["network"] = current
	}
	agents[agentID] = agent
	reg["agents"] = agents
	return agent, saveHubRegistry(stateDir, reg)
}

func createHubTask(stateDir string, agent obj, command string, args []string, batchID, originText string) (obj, error) {
	taskID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	task := obj{"kind": hubTaskKind, "version": version, "id": taskID, "agent_id": agent["id"], "agent_role": agent["role"], "command": command, "args": stringsToAny(args), "status": "queued", "created_at": now(), "batch_id": batchID, "origin_text": originText}
	if err := writeJSON(filepath.Join(hubTasksDir(stateDir), taskID+".json"), task, 0o600); err != nil {
		return nil, err
	}
	invalidateHubTaskCache(stateDir)
	return task, nil
}

func createHubTaskWithPayload(stateDir string, agent obj, command string, args []string, payload obj, batchID, originText string) (obj, error) {
	taskID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	task := obj{"kind": hubTaskKind, "version": version, "id": taskID, "agent_id": agent["id"], "agent_role": agent["role"], "command": command, "args": stringsToAny(args), "payload": payload, "status": "queued", "created_at": now(), "batch_id": batchID, "origin_text": originText}
	if err := writeJSON(filepath.Join(hubTasksDir(stateDir), taskID+".json"), task, 0o600); err != nil {
		return nil, err
	}
	invalidateHubTaskCache(stateDir)
	return task, nil
}

func mustRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func stringsToAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func loadHubTasksUncached(stateDir string) ([]obj, error) {
	dir := hubTasksDir(stateDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []obj{}, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []obj
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		task, err := loadJSON(path)
		if err != nil {
			return nil, err
		}
		if str(task["kind"]) == hubTaskKind {
			task["path"] = path
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return int64Value(tasks[i]["created_at"]) > int64Value(tasks[j]["created_at"]) })
	return tasks, nil
}

func buildHubTaskSummary(tasks []obj) hubTaskSummary {
	summary := hubTaskSummary{Total: len(tasks), RecentLine: recentOperationLine(tasks)}
	for _, task := range tasks {
		switch str(task["status"]) {
		case "queued":
			summary.Queued++
		case "running":
			summary.Running++
		case "done":
			summary.Done++
			summary.CompletedRecent++
		case "failed":
			summary.Failed++
			summary.CompletedRecent++
		case "cancelled":
			summary.Cancelled++
			summary.CompletedRecent++
		}
	}
	return summary
}

func getHubTaskCacheEntry(stateDir string) (hubTaskCacheEntry, error) {
	signature, err := hubTasksDirSignature(stateDir)
	if err != nil {
		return hubTaskCacheEntry{}, err
	}
	hubViewCache.Lock()
	if cached, ok := hubViewCache.tasks[stateDir]; ok && cached.signature == signature && hubCacheFresh(cached.cachedAt, hubTaskCacheMaxAge) {
		hubViewCache.Unlock()
		return cached, nil
	}
	hubViewCache.Unlock()
	tasks, err := loadHubTasksUncached(stateDir)
	if err != nil {
		return hubTaskCacheEntry{}, err
	}
	entry := hubTaskCacheEntry{signature: signature, tasks: cloneObjListDeep(tasks), summary: buildHubTaskSummary(tasks), cachedAt: time.Now()}
	hubViewCache.Lock()
	hubViewCache.tasks[stateDir] = entry
	hubViewCache.Unlock()
	return entry, nil
}

func listHubTasks(stateDir string) ([]obj, error) {
	entry, err := getHubTaskCacheEntry(stateDir)
	if err != nil {
		return nil, err
	}
	return cloneObjListDeep(entry.tasks), nil
}

func cachedHubTaskSummary(stateDir string) (hubTaskSummary, error) {
	entry, err := getHubTaskCacheEntry(stateDir)
	if err != nil {
		return hubTaskSummary{}, err
	}
	return entry.summary, nil
}

func cachedHubTaskResultsText(stateDir, batchID, agentID string) (string, error) {
	entry, err := getHubTaskCacheEntry(stateDir)
	if err != nil {
		return "", err
	}
	return formatHubTaskResultsTextForAgent(entry.tasks, batchID, agentID), nil
}

func listHubAlerts(stateDir string) ([]obj, error) {
	data, err := loadHubAlertsData(stateDir)
	if err != nil {
		return nil, err
	}
	alertsMap := asObj(data["alerts"])
	alerts := make([]obj, 0, len(alertsMap))
	for _, raw := range alertsMap {
		if alert := asObj(raw); len(alert) > 0 {
			alerts = append(alerts, alert)
		}
	}
	sort.Slice(alerts, func(i, j int) bool { return int64Value(alerts[i]["updated_at"]) > int64Value(alerts[j]["updated_at"]) })
	return alerts, nil
}

func recoverStaleHubTasks(stateDir string, leaseTimeoutSeconds, maxLeaseCount int64) (obj, error) {
	if leaseTimeoutSeconds <= 0 {
		leaseTimeoutSeconds = defaultTaskLeaseTimeoutSeconds
	}
	if maxLeaseCount <= 0 {
		maxLeaseCount = defaultTaskMaxLeaseCount
	}
	tasks, err := listHubTasks(stateDir)
	if err != nil {
		return nil, err
	}
	nowTs := now()
	requeuedTasks := []any{}
	failedTasks := []any{}
	checked := 0
	changed := false
	for _, task := range tasks {
		if str(task["status"]) != "running" {
			continue
		}
		checked++
		leasedAt := int64Value(task["leased_at"])
		if leasedAt <= 0 || nowTs-leasedAt < leaseTimeoutSeconds {
			continue
		}
		path := str(task["path"])
		delete(task, "path")
		leaseCount := int64Value(task["lease_count"])
		task["last_timeout_at"] = nowTs
		task["timeout_count"] = int64Value(task["timeout_count"]) + 1
		task["lease_timeout_seconds"] = leaseTimeoutSeconds
		if leaseCount >= maxLeaseCount {
			task["status"] = "failed"
			task["completed_at"] = nowTs
			task["result"] = obj{
				"success":               false,
				"error":                 fmt.Sprintf("task lease expired after %d attempt(s)", leaseCount),
				"lease_count":           leaseCount,
				"lease_timeout_seconds": leaseTimeoutSeconds,
			}
			failedTasks = append(failedTasks, firstNonEmpty(str(task["id"]), strings.TrimSuffix(filepath.Base(path), ".json")))
		} else {
			task["status"] = "queued"
			task["requeued_at"] = nowTs
			task["requeue_reason"] = "lease_timeout"
			requeuedTasks = append(requeuedTasks, firstNonEmpty(str(task["id"]), strings.TrimSuffix(filepath.Base(path), ".json")))
		}
		if err := writeJSON(path, task, 0o600); err != nil {
			return nil, err
		}
		changed = true
	}
	if changed {
		invalidateHubTaskCache(stateDir)
	}
	return obj{
		"checked_running":       checked,
		"requeued":              len(requeuedTasks),
		"failed":                len(failedTasks),
		"requeued_tasks":        requeuedTasks,
		"failed_tasks":          failedTasks,
		"lease_timeout_seconds": leaseTimeoutSeconds,
		"max_lease_count":       maxLeaseCount,
	}, nil
}

func leaseHubAgentTasks(stateDir, agentID string, limit int) ([]obj, error) {
	if _, err := recoverStaleHubTasks(stateDir, defaultTaskLeaseTimeoutSeconds, defaultTaskMaxLeaseCount); err != nil {
		return nil, err
	}
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return nil, err
	}
	dir := hubTasksDir(stateDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []obj{}, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var leased []obj
	changed := false
	for _, e := range entries {
		if len(leased) >= limit || e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		task, err := loadJSON(path)
		if err != nil {
			return nil, err
		}
		if str(task["kind"]) != hubTaskKind || str(task["agent_id"]) != agentID || str(task["status"]) != "queued" {
			continue
		}
		task["status"] = "running"
		task["leased_at"] = now()
		task["lease_count"] = int64Value(task["lease_count"]) + 1
		if err := writeJSON(path, task, 0o600); err != nil {
			return nil, err
		}
		changed = true
		delete(task, "path")
		leased = append(leased, task)
	}
	if changed {
		invalidateHubTaskCache(stateDir)
	}
	return leased, nil
}

func completeHubTask(stateDir, taskID, agentID string, result obj) (obj, error) {
	if _, err := ensureSafeName(taskID, "task.id"); err != nil {
		return nil, err
	}
	path := filepath.Join(hubTasksDir(stateDir), taskID+".json")
	task, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if str(task["agent_id"]) != agentID {
		return nil, errors.New("task does not belong to authenticated agent")
	}
	if ok, _ := result["success"].(bool); ok {
		task["status"] = "done"
	} else {
		task["status"] = "failed"
	}
	task["completed_at"] = now()
	if str(task["command"]) == "export_endpoint" && task["status"] == "done" {
		if err := storeHubLandingExport(stateDir, str(task["agent_id"]), asObj(result["endpoint"]), asObj(result["public_entries"])); err != nil {
			task["export_cache_error"] = err.Error()
		}
	}
	storedResult := result
	if str(task["command"]) == "export_endpoint" {
		storedResult = sanitizeTaskResult(result)
	}
	task["result"] = storedResult
	if str(task["command"]) == "sync_agent" && task["status"] == "done" {
		if err := storeHubSyncSnapshot(stateDir, agentID, result); err != nil {
			task["sync_store_error"] = err.Error()
		}
	}
	if str(task["command"]) == "bind_endpoint" {
		task["payload"] = sanitizeTaskPayload(asObj(task["payload"]))
	}
	if err := writeJSON(path, task, 0o600); err != nil {
		return nil, err
	}
	invalidateHubTaskCache(stateDir)
	if str(task["command"]) == "export_endpoint" {
		if task["status"] == "done" {
			if err := queueBindEndpointFromExportResult(stateDir, task, result); err != nil {
				task["followup_error"] = err.Error()
				if writeErr := writeJSON(path, task, 0o600); writeErr != nil {
					return nil, writeErr
				}
				invalidateHubTaskCache(stateDir)
			}
		}
		task["payload"] = sanitizeTaskPayload(asObj(task["payload"]))
		if writeErr := writeJSON(path, task, 0o600); writeErr != nil {
			return nil, writeErr
		}
		invalidateHubTaskCache(stateDir)
	}
	delete(task, "path")
	return task, nil
}

func sanitizeTaskPayload(payload obj) obj {
	if len(payload) == 0 {
		return payload
	}
	out := obj{}
	for k, v := range payload {
		if k == "endpoint" {
			out[k] = redactEndpoint(asObj(v))
			continue
		}
		if k == "mesh_landing" || k == "mesh_transit" {
			out[k] = redactMeshSpec(asObj(v))
			continue
		}
		out[k] = v
	}
	return out
}

func sanitizeTaskResult(result obj) obj {
	if len(result) == 0 {
		return result
	}
	out := obj{}
	for k, v := range result {
		if k == "endpoint" {
			out[k] = redactEndpoint(asObj(v))
			continue
		}
		out[k] = v
	}
	return out
}

func redactEndpoint(endpoint obj) obj {
	out := obj{}
	for k, v := range endpoint {
		if k == "password" || k == "username" {
			continue
		}
		out[k] = v
	}
	return out
}

func redactMeshSpec(mesh obj) obj {
	out := obj{}
	for k, v := range mesh {
		if k == "private_key" || k == "pre_shared_key" {
			continue
		}
		out[k] = v
	}
	return out
}

func queueBindEndpointFromExportResult(stateDir string, exportTask, result obj) error {
	endpoint := asObj(result["endpoint"])
	if len(endpoint) == 0 {
		return errors.New("export_endpoint result missing endpoint")
	}
	endpoint, err := validateEndpoint(endpoint, true)
	if err != nil {
		return err
	}
	link := asObj(exportTask["payload"])
	transitID := str(link["transit_id"])
	if transitID == "" {
		return errors.New("export task missing transit_id")
	}
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return err
	}
	transit, err := findHubAgentByID(agents, transitID)
	if err != nil {
		return err
	}
	if str(transit["role"]) != "transit" {
		return fmt.Errorf("%s is not a transit agent", transitID)
	}
	authUser := firstNonEmpty(str(link["auth_user"]), str(endpoint["name"]))
	if _, err := ensureSafeName(authUser, "auth_user"); err != nil {
		return err
	}
	payload := obj{
		"endpoint":      endpoint,
		"auth_user":     authUser,
		"landing_id":    link["landing_id"],
		"endpoint_name": endpoint["name"],
		"link_mode":     firstNonEmpty(str(link["link_mode"]), "direct"),
	}
	if str(payload["link_mode"]) == "mesh" {
		meshTransit := asObj(link["mesh_transit"])
		meshSummary := asObj(link["mesh"])
		landingIP := str(meshSummary["landing_ip"])
		if landingIP == "" {
			landingIP = strings.TrimSuffix(str(asObj(link["mesh_landing"])["address"]), "/32")
		}
		if landingIP == "" {
			return errors.New("mesh link missing landing overlay ip")
		}
		if str(meshTransit["peer_endpoint"]) == "" {
			if publicEndpoint, err := publicWireGuardEndpointFromExportResult(result); err != nil {
				return err
			} else if publicEndpoint != "" {
				meshTransit["peer_endpoint"] = publicEndpoint
			} else {
				meshTransit["peer_endpoint"] = net.JoinHostPort(str(endpoint["server"]), strconv.Itoa(int(int64Value(firstNonNil(meshTransit["peer_endpoint_port"], meshSummary["port"])))))
			}
		}
		endpoint["direct_server"] = endpoint["server"]
		endpoint["direct_server_port"] = endpoint["server_port"]
		endpoint["server"] = landingIP
		endpoint["link_mode"] = "mesh"
		endpoint["mesh"] = obj{
			"interface":  meshSummary["interface"],
			"transit_ip": meshSummary["transit_ip"],
			"landing_ip": meshSummary["landing_ip"],
		}
		if _, err := validateEndpoint(endpoint, true); err != nil {
			return err
		}
		payload["endpoint"] = endpoint
		payload["mesh_transit"] = meshTransit
		payload["mesh"] = meshSummary
	}
	if inboundTag := str(link["inbound_tag"]); inboundTag != "" {
		payload["inbound_tag"] = inboundTag
	}
	if flow := str(link["flow"]); flow != "" {
		payload["flow"] = flow
	}
	_, err = createHubTaskWithPayload(stateDir, transit, "bind_endpoint", []string{str(endpoint["name"]), authUser}, payload, str(exportTask["batch_id"]), str(exportTask["origin_text"]))
	return err
}

func cancelAgentTasks(stateDir, agentID, reason string) (int, error) {
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return 0, err
	}
	tasks, err := listHubTasks(stateDir)
	if err != nil {
		return 0, err
	}
	cancelled := 0
	for _, task := range tasks {
		if str(task["agent_id"]) != agentID {
			continue
		}
		status := str(task["status"])
		if status != "queued" && status != "running" {
			continue
		}
		task["status"] = "cancelled"
		task["cancelled_at"] = now()
		task["cancel_reason"] = reason
		path := str(task["path"])
		delete(task, "path")
		if err := writeJSON(path, task, 0o600); err != nil {
			return cancelled, err
		}
		cancelled++
	}
	if cancelled > 0 {
		invalidateHubTaskCache(stateDir)
	}
	return cancelled, nil
}

func removeHubAgent(stateDir, agentID, reason string, tombstone, cancelTasks bool) (obj, error) {
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return nil, err
	}
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	agents := asObj(reg["agents"])
	agent := asObj(agents[agentID])
	if len(agent) == 0 {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	cleanup, err := queueRemovalCleanupTasks(stateDir, agent, agents, reason)
	if err != nil {
		return nil, err
	}
	delete(agents, agentID)
	reg["agents"] = agents
	if err := saveHubRegistry(stateDir, reg); err != nil {
		return nil, err
	}
	cancelled := 0
	if cancelTasks {
		cancelled, err = cancelAgentTasks(stateDir, agentID, reason)
		if err != nil {
			return nil, err
		}
	}
	tombstonePath := ""
	if tombstone {
		removed, err := loadRemovedAgents(stateDir)
		if err != nil {
			return nil, err
		}
		removedAgents := asObj(removed["agents"])
		removedAgents[agentID] = obj{
			"id":              agentID,
			"removed_at":      now(),
			"reason":          reason,
			"agent":           summarizeAgent(agent),
			"cleanup":         cleanup,
			"cancelled_tasks": cancelled,
		}
		removed["agents"] = removedAgents
		if err := saveRemovedAgents(stateDir, removed); err != nil {
			return nil, err
		}
		tombstonePath = hubRemovedAgentsPath(stateDir)
	}
	return obj{
		"removed":         agentID,
		"role":            agent["role"],
		"reason":          reason,
		"cleanup":         cleanup,
		"cancelled_tasks": cancelled,
		"tombstone":       tombstonePath,
	}, nil
}

func queueRemovalCleanupTasks(stateDir string, removed obj, agents obj, reason string) (obj, error) {
	removedID := str(removed["id"])
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	originText := fmt.Sprintf("remove-agent %s", removedID)
	queued := []any{}
	warnings := []any{}
	switch str(removed["role"]) {
	case "landing":
		for id, raw := range agents {
			if id == removedID {
				continue
			}
			transit := asObj(raw)
			if str(transit["role"]) != "transit" {
				continue
			}
			for _, link := range matchingTransitLinksForLanding(transit, removed) {
				payload := cleanupPayloadForLink(removed, link, reason)
				if _, err := createHubTaskWithPayload(stateDir, transit, "unbind_endpoint", []string{}, payload, batchID, originText); err != nil {
					return nil, err
				}
				queued = append(queued, obj{"agent_id": transit["id"], "command": "unbind_endpoint", "endpoint_name": payload["endpoint_name"], "auth_user": payload["auth_user"]})
			}
		}
	case "transit":
		links := asList(asObj(removed["topology"])["links"])
		for id, raw := range agents {
			if id == removedID {
				continue
			}
			landing := asObj(raw)
			if str(landing["role"]) != "landing" {
				continue
			}
			for _, rawLink := range links {
				link := asObj(rawLink)
				if !transitLinkMatchesLanding(link, landing) || str(link["link_mode"]) != "mesh" {
					continue
				}
				payload := meshTeardownPayloadForLink(removed, landing, link, reason)
				if str(payload["interface"]) == "" {
					warnings = append(warnings, obj{"agent_id": landing["id"], "warning": "mesh interface unknown; landing cleanup must be manual"})
					continue
				}
				if _, err := createHubTaskWithPayload(stateDir, landing, "teardown_mesh", []string{}, payload, batchID, originText); err != nil {
					return nil, err
				}
				queued = append(queued, obj{"agent_id": landing["id"], "command": "teardown_mesh", "interface": payload["interface"]})
			}
		}
	}
	return obj{"queued": queued, "warnings": warnings, "batch_id": batchID}, nil
}

func matchingTransitLinksForLanding(transit, landing obj) []obj {
	var out []obj
	for _, raw := range asList(asObj(transit["topology"])["links"]) {
		link := asObj(raw)
		if transitLinkMatchesLanding(link, landing) {
			out = append(out, link)
		}
	}
	return out
}

func cleanupPayloadForLink(landing, link obj, reason string) obj {
	payload := obj{
		"reason":           reason,
		"removed_agent_id": str(landing["id"]),
		"removed_role":     "landing",
		"auth_user":        link["auth_user"],
		"endpoint_name":    link["endpoint_name"],
		"outbound_tag":     link["outbound_tag"],
		"server":           link["server"],
		"server_port":      link["server_port"],
	}
	if mesh := asObj(link["mesh"]); len(mesh) > 0 {
		payload["mesh"] = mesh
		payload["teardown_mesh"] = true
	}
	return payload
}

func meshTeardownPayloadForLink(transit, landing, link obj, reason string) obj {
	mesh := asObj(link["mesh"])
	iface := str(mesh["interface"])
	if iface == "" && str(link["endpoint_name"]) != "" {
		iface = meshInterfaceName(str(transit["id"]) + "|" + str(landing["id"]) + "|" + str(link["endpoint_name"]))
	}
	return obj{
		"reason":           reason,
		"removed_agent_id": str(transit["id"]),
		"removed_role":     "transit",
		"interface":        iface,
		"config_dir":       firstNonEmpty(str(mesh["config_dir"]), defaultMeshConfigDir),
		"mesh":             mesh,
	}
}

func hubDispatchCommand(stateDir, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || !strings.HasPrefix(text, "/") {
		return "", errors.New("hub command must start with /")
	}
	parts := strings.Fields(text)
	rawCommand := strings.TrimPrefix(strings.Split(parts[0], "@")[0], "/")
	command := normalizeTelegramCommand(parts[0])
	args := parts[1:]
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return "", err
	}
	switch command {
	case "help", "start":
		return hubTelegramCommands(), nil
	case "tasks":
		tasks, err := listHubTasks(stateDir)
		if err != nil {
			return "", err
		}
		return formatHubTasksText(tasks), nil
	case "results":
		batch := ""
		if len(args) > 0 {
			batch = args[0]
		}
		return cachedHubTaskResultsText(stateDir, batch, "")
	case "agents":
		return formatAgentsText(agents), nil
	case "topology", "tree":
		return cachedHubTopologyText(stateDir)
	case "sync":
		if len(args) == 0 {
			return "", errors.New("usage: /sync <agent_id|all>")
		}
		if args[0] == "all" {
			res, err := hubSyncAll(stateDir)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("已下发节点详情刷新：批次 %s，任务 %d 个", str(res["batch_id"]), len(asList(res["queued"]))), nil
		}
		res, err := hubSyncAgent(stateDir, args[0])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已下发节点详情刷新：%s -> %s", str(res["agent_id"]), str(res["queued"])), nil
	case "link", "connect":
		return hubLinkTransitLanding(stateDir, agents, args, text)
	case "probe", "probe_link", "check_link", "link_probe", "link_check":
		return hubProbeLink(stateDir, agents, args, text)
	case "up", "uphub", "upall", "uptransit", "uplanding":
		if !strings.HasPrefix(strings.ToLower(rawCommand), "relaypilot_") {
			return "", fmt.Errorf("unsupported hub command: /%s (use /relaypilot_%s)", rawCommand, command)
		}
		updateArgs, help := compactUpdateArgs(command, args)
		if help {
			return compactUpdateHelp(), nil
		}
		return hubUpdateCommandWithDefaults(stateDir, agents, updateArgs, text, "latest", true)
	case "update", "upgrade":
		return hubUpdateCommand(stateDir, agents, args, text)
	case "decommission", "retire":
		return hubDecommissionCommand(stateDir, agents, args, text)
	case "alerts":
		alerts, err := listHubAlerts(stateDir)
		if err != nil {
			return "", err
		}
		return formatHubAlertsText(alerts), nil
	case "status":
		if len(args) == 0 || args[0] == "hub" {
			return hubStatusText(stateDir, agents), nil
		}
	case "doctor":
		if len(args) == 0 || args[0] == "hub" {
			return strings.Join([]string{
				"hub doctor:",
				"state_dir=" + stateDir,
				"registry=" + hubAgentsPath(stateDir),
				fmt.Sprintf("agents=%d", len(agents)),
			}, "\n"), nil
		}
	}
	if command != "status" && command != "doctor" && command != "endpoints" && command != "show_endpoint" && command != "inspect_conf" {
		return "", fmt.Errorf("unsupported hub command: /%s", command)
	}
	selector := ""
	commandArgs := []string{}
	if len(args) > 0 {
		selector = args[0]
		commandArgs = args[1:]
	} else if command == "endpoints" {
		selector = "all"
	}
	if command == "show_endpoint" {
		if len(args) < 2 {
			return "", errors.New("usage: /show_endpoint <agent_id> <endpoint_name>")
		}
		selector = args[0]
		commandArgs = args[1:]
	}
	if command == "inspect_conf" {
		if selector == "" {
			return "", errors.New("usage: /inspect_conf <agent_id> [path]")
		}
		commandArgs = args[1:]
	}
	matched := selectAgents(agents, selector)
	if len(matched) == 0 {
		return "", fmt.Errorf("no agents matched selector: %s", selector)
	}
	matched, skippedPending := splitPendingAgents(matched)
	if len(matched) == 0 {
		return "", fmt.Errorf("matched agents are still pending enrollment: %s", selector)
	}
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	var b strings.Builder
	fmt.Fprintf(&b, "📨 已下发 /%s 给 %d 个节点\n", command, len(matched))
	fmt.Fprintf(&b, "目标：%s\n", selector)
	if len(skippedPending) > 0 {
		fmt.Fprintf(&b, "已跳过：%d 个待接入节点\n", len(skippedPending))
	}
	fmt.Fprintf(&b, "批次：%s\n", batchID)
	b.WriteString("Hub 会汇总结果后统一回复；agent 不会直接回复 Telegram。\n")
	for _, agent := range matched {
		if _, err := createHubTask(stateDir, agent, command, commandArgs, batchID, text); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s %s · %s\n", roleIcon(str(agent["role"])), str(agent["id"]), str(agent["name"]))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func normalizeDecommissionMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "detach", "leave", "leave-hub":
		return "detach", nil
	case "purge", "purge-managed-proxy", "reset-agent", "reset-proxy":
		return "purge-managed-proxy", nil
	case "uninstall", "full":
		return "uninstall", nil
	default:
		return "", fmt.Errorf("unsupported decommission mode: %s", mode)
	}
}

func parseDecommissionArgs(args []string) (agentID, mode, confirm string, dryRun bool, err error) {
	mode = "uninstall"
	dryRun = true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--mode":
			if i+1 >= len(args) {
				return "", "", "", true, errors.New("--mode requires a value")
			}
			mode = args[i+1]
			i++
		case "--dry-run", "--preview":
			dryRun = true
		case "--confirm":
			if i+1 >= len(args) {
				return "", "", "", true, errors.New("--confirm requires the exact agent id")
			}
			confirm = args[i+1]
			dryRun = false
			i++
		default:
			if strings.HasPrefix(args[i], "--mode=") {
				mode = strings.TrimPrefix(args[i], "--mode=")
				continue
			}
			if strings.HasPrefix(args[i], "--confirm=") {
				confirm = strings.TrimPrefix(args[i], "--confirm=")
				dryRun = false
				continue
			}
			if strings.HasPrefix(args[i], "--") {
				return "", "", "", true, fmt.Errorf("unexpected decommission argument: %s", args[i])
			}
			if agentID == "" {
				agentID = args[i]
			} else {
				return "", "", "", true, fmt.Errorf("unexpected decommission argument: %s", args[i])
			}
		}
	}
	if agentID == "" {
		return "", "", "", true, errors.New("usage: /decommission <agent_id> [--mode detach|purge-managed-proxy|uninstall] [--confirm agent_id]")
	}
	mode, err = normalizeDecommissionMode(mode)
	if err != nil {
		return "", "", "", true, err
	}
	if !dryRun && confirm != agentID {
		return "", "", "", true, fmt.Errorf("--confirm must exactly match agent id: %s", agentID)
	}
	return agentID, mode, confirm, dryRun, nil
}

func decommissionModeLabel(mode string) string {
	switch mode {
	case "detach":
		return "退出 Hub 托管"
	case "purge-managed-proxy":
		return "清理 RelayPilot 托管代理配置"
	case "uninstall":
		return "彻底卸载 RelayPilot 与托管代理配置"
	default:
		return mode
	}
}

func hubDecommissionCommand(stateDir string, agents []obj, args []string, originText string) (string, error) {
	agentID, mode, _, dryRun, err := parseDecommissionArgs(args)
	if err != nil {
		return "", err
	}
	agent, err := findHubAgentByID(agents, agentID)
	if err != nil {
		return "", err
	}
	if str(agent["status"]) == "pending" {
		return "", fmt.Errorf("agent is still pending enrollment: %s", agentID)
	}
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	payload := obj{
		"mode":          mode,
		"dry_run":       dryRun,
		"delay_seconds": 12,
	}
	if _, err := createHubTaskWithPayload(stateDir, agent, "decommission_agent", []string{mode}, payload, batchID, originText); err != nil {
		return "", err
	}
	lines := []string{}
	if dryRun {
		lines = append(lines,
			"🧯 已下发远程退役预览",
			"目标："+agentID,
			"模式："+decommissionModeLabel(mode),
			"批次："+batchID,
			"节点本机授权：必须已开启 allow_remote_decommission=true，否则 Agent 会拒绝。",
			"预览不删除文件；确认执行需追加："+"/decommission "+agentID+" --mode "+mode+" --confirm "+agentID,
		)
	} else {
		lines = append(lines,
			"🧯 已下发远程退役",
			"目标："+agentID,
			"模式："+decommissionModeLabel(mode),
			"批次："+batchID,
			"Agent 会先回报结果，再延迟执行本机清理；清理后该节点将停止回连。",
		)
	}
	return strings.Join(lines, "\n"), nil
}

func parseUpdateArgs(args []string) (selector, updateVersion string, restart bool, err error) {
	return parseUpdateArgsWithDefaults(args, "", false)
}

func parseUpdateArgsWithDefaults(args []string, defaultVersion string, defaultRestart bool) (selector, updateVersion string, restart bool, err error) {
	restart = defaultRestart
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--version":
			if i+1 >= len(args) {
				return "", "", false, errors.New("--version requires a value")
			}
			updateVersion = args[i+1]
			i++
		case "--restart", "--restart-services":
			restart = true
		case "--no-restart", "--no-restart-services":
			restart = false
		default:
			if selector == "" {
				selector = args[i]
			} else if updateVersion == "" {
				updateVersion = args[i]
			} else {
				return "", "", false, fmt.Errorf("unexpected update argument: %s", args[i])
			}
		}
	}
	selector = strings.TrimSpace(selector)
	updateVersion = strings.TrimSpace(updateVersion)
	if updateVersion == "" {
		updateVersion = defaultVersion
	}
	if selector == "" || updateVersion == "" {
		return "", "", false, errors.New("usage: /update <hub|all|transit|landing|agent_id> <version|latest> [--restart]")
	}
	return selector, updateVersion, restart, nil
}

func hubUpdateCommand(stateDir string, agents []obj, args []string, originText string) (string, error) {
	return hubUpdateCommandWithDefaults(stateDir, agents, args, originText, "", false)
}

func hubUpdateCommandWithDefaults(stateDir string, agents []obj, args []string, originText, defaultVersion string, defaultRestart bool) (string, error) {
	selector, updateVersion, restart, err := parseUpdateArgsWithDefaults(args, defaultVersion, defaultRestart)
	if err != nil {
		return "", err
	}
	if selector == "hub" {
		out, err := runRelayPilotUpdate(updateVersion)
		if err != nil {
			return "", err
		}
		lines := []string{
			"✅ Hub 已更新 RelayPilot",
			"版本：" + updateVersion,
		}
		if trimmed := lastNonEmptyLines(out, 4); trimmed != "" {
			lines = append(lines, trimmed)
		}
		if restart {
			if err := scheduleServiceRestart([]string{envOrDefault("RELAYPILOT_HUB_SERVICE_NAME", "relaypilot-hub"), envOrDefault("RELAYPILOT_BOT_SERVICE_NAME", "relaypilot-bot")}, 12); err != nil {
				lines = append(lines, "⚠️ 重启计划失败："+err.Error())
			} else {
				lines = append(lines, "将在约 12 秒后重启 Hub/Bot 服务。")
			}
		} else {
			lines = append(lines, "常驻 Hub/Bot 服务需重启后才使用新版本。")
		}
		lines = append(lines, "Telegram 命令菜单如仍显示旧项：relaypilot bot register --hub")
		return strings.Join(lines, "\n"), nil
	}
	matched := selectAgents(agents, selector)
	if len(matched) == 0 {
		return "", fmt.Errorf("no agents matched selector: %s", selector)
	}
	matched, skippedPending := splitPendingAgents(matched)
	if len(matched) == 0 {
		return "", fmt.Errorf("matched agents are still pending enrollment: %s", selector)
	}
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	payload := obj{"version": updateVersion, "restart_services": restart}
	var b strings.Builder
	fmt.Fprintf(&b, "📦 已下发 RelayPilot 更新给 %d 个节点\n", len(matched))
	fmt.Fprintf(&b, "目标：%s\n", selector)
	fmt.Fprintf(&b, "版本：%s\n", updateVersion)
	fmt.Fprintf(&b, "重启服务：%s\n", yesNo(restart))
	if len(skippedPending) > 0 {
		fmt.Fprintf(&b, "已跳过：%d 个待接入节点\n", len(skippedPending))
	}
	fmt.Fprintf(&b, "批次：%s\n", batchID)
	b.WriteString("Hub 会汇总结果后统一回复；建议先单节点 canary，再更新 all。\n")
	for _, agent := range matched {
		if _, err := createHubTaskWithPayload(stateDir, agent, "self_update", []string{updateVersion}, payload, batchID, originText); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s %s · %s\n", roleIcon(str(agent["role"])), str(agent["id"]), str(agent["name"]))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func compactUpdateArgs(command string, args []string) ([]string, bool) {
	command = normalizeTelegramCommand(command)
	if command == "up" {
		if len(args) == 0 {
			return nil, true
		}
		return args, false
	}
	prefix := ""
	switch command {
	case "uphub":
		prefix = "hub"
	case "upall":
		prefix = "all"
	case "uptransit":
		prefix = "transit"
	case "uplanding":
		prefix = "landing"
	}
	if prefix == "" {
		return args, false
	}
	return append([]string{prefix}, args...), false
}

func compactUpdateHelp() string {
	return strings.Join([]string{
		"⬆️ RelayPilot 更新短命令",
		"默认：latest + 重启服务",
		"",
		"/relaypilot_uphub",
		"/relaypilot_up transit-hk",
		"/relaypilot_up transit",
		"/relaypilot_up landing",
		"/relaypilot_upall",
		"",
		"指定版本：/relaypilot_upall v0.1.12",
		"不重启：/relaypilot_upall v0.1.12 --no-restart",
		"建议：先单节点 canary，再 all。",
	}, "\n")
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func relayPilotCLIPath() (string, error) {
	candidates := []string{
		strings.TrimSpace(os.Getenv("RELAYPILOT_CLI")),
		strings.TrimSpace(os.Getenv("RELAYPILOT_BIN_PATH")),
		"/usr/local/bin/relaypilot",
		"/opt/relaypilot/relaypilot.sh",
	}
	if looked, err := exec.LookPath("relaypilot"); err == nil {
		candidates = append(candidates, looked)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("relaypilot CLI not found; set RELAYPILOT_CLI or install /usr/local/bin/relaypilot")
}

func runRelayPilotUpdate(updateVersion string) (string, error) {
	cli, err := relayPilotCLIPath()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "update", "--version", updateVersion, "--no-restart-services")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), errors.New("relaypilot update timed out")
	}
	if err != nil {
		return string(out), fmt.Errorf("relaypilot update failed: %w: %s", err, lastNonEmptyLines(string(out), 8))
	}
	return string(out), nil
}

func lastNonEmptyLines(text string, maxLines int) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func validServiceName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == '@' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func scheduleServiceRestart(services []string, delaySeconds int) error {
	if delaySeconds < 1 {
		delaySeconds = 1
	}
	var valid []string
	seen := map[string]bool{}
	for _, svc := range services {
		svc = strings.TrimSpace(svc)
		if !validServiceName(svc) || seen[svc] {
			continue
		}
		seen[svc] = true
		valid = append(valid, svc)
	}
	if len(valid) == 0 {
		return errors.New("no valid services to restart")
	}
	script := fmt.Sprintf("sleep %d\n", delaySeconds)
	if _, err := exec.LookPath("systemctl"); err == nil {
		script += "systemctl restart " + strings.Join(valid, " ") + " >/dev/null 2>&1 || true\n"
	} else if _, err := exec.LookPath("rc-service"); err == nil {
		for _, svc := range valid {
			script += "rc-service " + svc + " restart >/dev/null 2>&1 || true\n"
		}
	} else {
		return errors.New("systemctl/rc-service not found")
	}
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

func linkProbeTimeoutSeconds(v int64) int {
	if v <= 0 {
		return defaultLinkProbeTimeoutSeconds
	}
	if v < 1 {
		return 1
	}
	if v > maxLinkProbeTimeoutSeconds {
		return maxLinkProbeTimeoutSeconds
	}
	return int(v)
}

func parseLinkProbeArgs(args []string) (transitID, landingID, endpointName string, timeoutSeconds int, err error) {
	timeoutSeconds = defaultLinkProbeTimeoutSeconds
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--timeout" || arg == "--timeout-seconds":
			if i+1 >= len(args) {
				return "", "", "", 0, errors.New("--timeout requires seconds")
			}
			n, parseErr := strconv.ParseInt(args[i+1], 10, 64)
			if parseErr != nil {
				return "", "", "", 0, fmt.Errorf("--timeout must be an integer: %w", parseErr)
			}
			timeoutSeconds = linkProbeTimeoutSeconds(n)
			i++
		case strings.HasPrefix(arg, "--timeout="):
			n, parseErr := strconv.ParseInt(strings.TrimPrefix(arg, "--timeout="), 10, 64)
			if parseErr != nil {
				return "", "", "", 0, fmt.Errorf("--timeout must be an integer: %w", parseErr)
			}
			timeoutSeconds = linkProbeTimeoutSeconds(n)
		case strings.HasPrefix(arg, "--timeout-seconds="):
			n, parseErr := strconv.ParseInt(strings.TrimPrefix(arg, "--timeout-seconds="), 10, 64)
			if parseErr != nil {
				return "", "", "", 0, fmt.Errorf("--timeout-seconds must be an integer: %w", parseErr)
			}
			timeoutSeconds = linkProbeTimeoutSeconds(n)
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) < 2 {
		return "", "", "", 0, errors.New("usage: /probe <transit_id> <landing_id> [endpoint_name] [--timeout seconds]")
	}
	transitID, landingID = positional[0], positional[1]
	if len(positional) >= 3 {
		endpointName = positional[2]
	}
	if len(positional) > 3 {
		return "", "", "", 0, fmt.Errorf("unexpected probe argument: %s", positional[3])
	}
	return transitID, landingID, endpointName, timeoutSeconds, nil
}

func landingEndpointSnapshot(landing obj, endpointName string) obj {
	for _, raw := range asList(asObj(landing["topology"])["endpoints"]) {
		endpoint := asObj(raw)
		if endpointName == "" || str(endpoint["name"]) == endpointName {
			return endpoint
		}
	}
	return nil
}

func probeTargetFromHubSnapshots(transit, landing obj, endpointName string) (obj, error) {
	var selectedLink obj
	for _, link := range matchingTransitLinksForLanding(transit, landing) {
		if endpointName != "" && str(link["endpoint_name"]) != "" && str(link["endpoint_name"]) != endpointName {
			continue
		}
		host := str(link["server"])
		port := int64Value(link["server_port"])
		if host == "" || port <= 0 {
			selectedLink = link
			continue
		}
		target := obj{
			"host":          host,
			"port":          port,
			"endpoint_name": firstNonEmpty(endpointName, str(link["endpoint_name"])),
			"outbound_tag":  link["outbound_tag"],
			"auth_user":     link["auth_user"],
			"source":        "transit_link",
		}
		if mode := str(link["link_mode"]); mode != "" {
			target["link_mode"] = mode
		}
		return target, nil
	}
	endpoint := landingEndpointSnapshot(landing, endpointName)
	if len(endpoint) == 0 {
		if selectedLink != nil {
			return nil, errors.New("matched transit link is missing probe host/port and landing endpoint snapshot is unavailable")
		}
		return nil, fmt.Errorf("landing endpoint snapshot not found: %s", firstNonEmpty(endpointName, "(first endpoint)"))
	}
	host := str(endpoint["server"])
	port := int64Value(endpoint["server_port"])
	if host == "" || port <= 0 {
		return nil, fmt.Errorf("landing endpoint %s is missing server/server_port", str(endpoint["name"]))
	}
	return obj{
		"host":          host,
		"port":          port,
		"endpoint_name": str(endpoint["name"]),
		"outbound_tag":  endpoint["tag"],
		"source":        "landing_endpoint",
	}, nil
}

func hubProbeLink(stateDir string, agents []obj, args []string, originText string) (string, error) {
	transitID, landingID, endpointName, timeoutSeconds, err := parseLinkProbeArgs(args)
	if err != nil {
		return "", err
	}
	transit, err := findHubAgentByID(agents, transitID)
	if err != nil {
		return "", err
	}
	landing, err := findHubAgentByID(agents, landingID)
	if err != nil {
		return "", err
	}
	if str(transit["role"]) != "transit" {
		return "", fmt.Errorf("%s is not a transit agent", transitID)
	}
	if str(landing["role"]) != "landing" {
		return "", fmt.Errorf("%s is not a landing agent", landingID)
	}
	target, err := probeTargetFromHubSnapshots(transit, landing, endpointName)
	if err != nil {
		return "", err
	}
	port := int64Value(target["port"])
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid probe port: %v", target["port"])
	}
	selectedEndpoint := firstNonEmpty(str(target["endpoint_name"]), endpointName)
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	payload := obj{
		"transit_id":      transitID,
		"landing_id":      landingID,
		"endpoint_name":   selectedEndpoint,
		"host":            str(target["host"]),
		"port":            port,
		"timeout_seconds": timeoutSeconds,
		"target_source":   target["source"],
	}
	for _, key := range []string{"outbound_tag", "auth_user", "link_mode"} {
		if str(target[key]) != "" {
			payload[key] = target[key]
		}
	}
	if _, err := createHubTaskWithPayload(stateDir, transit, "probe_link", []string{landingID, selectedEndpoint}, payload, batchID, originText); err != nil {
		return "", err
	}
	lines := []string{
		"🧪 已下发链路检测",
		fmt.Sprintf("链路：%s → %s", transitID, landingID),
		fmt.Sprintf("目标：%s", net.JoinHostPort(str(target["host"]), strconv.Itoa(int(port)))),
		fmt.Sprintf("超时：%ds", timeoutSeconds),
		fmt.Sprintf("批次：%s", batchID),
		"资源：单次 TCP 探测，不会启动后台监控或持续轮询。",
		"结果稍后在“最近操作”查看。",
	}
	return strings.Join(lines, "\n"), nil
}

func hubLinkTransitLanding(stateDir string, agents []obj, args []string, originText string) (string, error) {
	if len(args) < 2 {
		return "", errors.New("usage: /link <transit_id> <landing_id> [auth_user] [endpoint_name] [--mode direct|mesh] [--inbound-tag tag] [--flow flow]")
	}
	var positional []string
	inboundTag := ""
	flow := ""
	authUserFlag := ""
	endpointNameFlag := ""
	linkMode := "direct"
	meshCIDR := ""
	meshIface := ""
	meshPort := 0
	meshEndpoint := ""
	meshKeepalive := defaultMeshKeepaliveSeconds
	meshConfigDir := defaultMeshConfigDir
	meshAutoUp := true
	applyDataPlane := true
	restartService := true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--auth-user":
			if i+1 >= len(args) {
				return "", errors.New("--auth-user requires a value")
			}
			authUserFlag = args[i+1]
			i++
		case "--endpoint-name":
			if i+1 >= len(args) {
				return "", errors.New("--endpoint-name requires a value")
			}
			endpointNameFlag = args[i+1]
			i++
		case "--mode", "--link-mode":
			if i+1 >= len(args) {
				return "", errors.New("--mode requires direct or mesh")
			}
			mode, err := normalizeLinkMode(args[i+1])
			if err != nil {
				return "", err
			}
			linkMode = mode
			i++
		case "--direct":
			linkMode = "direct"
		case "--mesh", "--auto-network", "--overlay":
			linkMode = "mesh"
		case "--mesh-cidr":
			if i+1 >= len(args) {
				return "", errors.New("--mesh-cidr requires a value")
			}
			meshCIDR = args[i+1]
			i++
		case "--mesh-iface":
			if i+1 >= len(args) {
				return "", errors.New("--mesh-iface requires a value")
			}
			meshIface = args[i+1]
			i++
		case "--mesh-port":
			if i+1 >= len(args) {
				return "", errors.New("--mesh-port requires a value")
			}
			port, err := strconv.Atoi(args[i+1])
			if err != nil {
				return "", fmt.Errorf("--mesh-port must be an integer: %w", err)
			}
			meshPort = port
			i++
		case "--mesh-endpoint":
			if i+1 >= len(args) {
				return "", errors.New("--mesh-endpoint requires a value")
			}
			meshEndpoint = args[i+1]
			i++
		case "--mesh-keepalive":
			if i+1 >= len(args) {
				return "", errors.New("--mesh-keepalive requires a value")
			}
			keepalive, err := strconv.Atoi(args[i+1])
			if err != nil {
				return "", fmt.Errorf("--mesh-keepalive must be an integer: %w", err)
			}
			meshKeepalive = keepalive
			i++
		case "--mesh-config-dir":
			if i+1 >= len(args) {
				return "", errors.New("--mesh-config-dir requires a value")
			}
			meshConfigDir = args[i+1]
			i++
		case "--mesh-no-up":
			meshAutoUp = false
		case "--no-apply", "--no-dataplane-apply":
			applyDataPlane = false
		case "--apply", "--apply-dataplane":
			applyDataPlane = true
		case "--no-restart", "--no-restart-service", "--no-restart-services":
			restartService = false
		case "--restart", "--restart-service", "--restart-services":
			restartService = true
		case "--inbound-tag":
			if i+1 >= len(args) {
				return "", errors.New("--inbound-tag requires a value")
			}
			inboundTag = args[i+1]
			i++
		case "--flow":
			if i+1 >= len(args) {
				return "", errors.New("--flow requires a value")
			}
			flow = args[i+1]
			i++
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) < 2 {
		return "", errors.New("usage: /link <transit_id> <landing_id> [auth_user] [endpoint_name] [--mode direct|mesh] [--inbound-tag tag] [--flow flow]")
	}
	transitID, landingID := positional[0], positional[1]
	authUser := ""
	if len(positional) >= 3 {
		authUser = positional[2]
	}
	if authUserFlag != "" {
		authUser = authUserFlag
	}
	endpointName := ""
	if len(positional) >= 4 {
		endpointName = positional[3]
	}
	if endpointNameFlag != "" {
		endpointName = endpointNameFlag
	}
	if authUser != "" {
		if _, err := ensureSafeName(authUser, "auth_user"); err != nil {
			return "", err
		}
	}
	if inboundTag != "" {
		if _, err := ensureSafeTag(inboundTag, "inbound_tag"); err != nil {
			return "", err
		}
	}
	transit, err := findHubAgentByID(agents, transitID)
	if err != nil {
		return "", err
	}
	landing, err := findHubAgentByID(agents, landingID)
	if err != nil {
		return "", err
	}
	if str(transit["role"]) != "transit" {
		return "", fmt.Errorf("%s is not a transit agent", transitID)
	}
	if str(landing["role"]) != "landing" {
		return "", fmt.Errorf("%s is not a landing agent", landingID)
	}
	if endpointName == "" {
		endpointName = firstLandingEndpointName(landing)
	}
	if endpointName == "" {
		refresh, refreshErr := queueHubDetailRefreshForAgents(stateDir, []obj{transit, landing}, originText, "missing landing endpoint snapshot")
		if refreshErr != nil {
			return "", fmt.Errorf("landing %s has no endpoint snapshot and detail refresh could not be queued: %w", landingID, refreshErr)
		}
		return strings.Join([]string{
			"需要刷新节点详情",
			fmt.Sprintf("链路：%s → %s", transitID, landingID),
			fmt.Sprintf("已下发节点详情刷新：批次 %s，任务 %d 个", str(refresh["batch_id"]), len(asList(refresh["queued"]))),
			"刷新完成后再次串联。",
		}, "\n"), nil
	}
	if _, err := normalizeLinkMode(linkMode); err != nil {
		return "", err
	}
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	payload := obj{
		"transit_id":      transitID,
		"landing_id":      landingID,
		"endpoint_name":   endpointName,
		"auth_user":       firstNonEmpty(authUser, endpointName),
		"link_mode":       linkMode,
		"apply_dataplane": applyDataPlane,
		"restart_service": restartService,
	}
	if inboundTag != "" {
		payload["inbound_tag"] = inboundTag
	}
	if flow != "" {
		payload["flow"] = flow
	}
	if linkMode == "mesh" {
		mesh, err := makeMeshLinkPayload(transitID, landingID, endpointName, meshCIDR, meshIface, meshPort, meshEndpoint, meshKeepalive, meshConfigDir, meshAutoUp)
		if err != nil {
			return "", err
		}
		payload["mesh_landing"] = mesh["landing"]
		payload["mesh_transit"] = mesh["transit"]
		payload["mesh"] = mesh["summary"]
	}
	if _, err := createHubTaskWithPayload(stateDir, landing, "export_endpoint", []string{endpointName}, payload, batchID, originText); err != nil {
		return "", err
	}
	lines := []string{
		"🔗 已下发串联任务",
		fmt.Sprintf("链路：%s → %s", transitID, landingID),
		fmt.Sprintf("模式：%s", formatLinkMode(linkMode)),
		fmt.Sprintf("落地 endpoint：%s", endpointName),
		fmt.Sprintf("auth_user：%s", payload["auth_user"]),
		fmt.Sprintf("批次：%s", batchID),
	}
	if linkMode == "mesh" {
		summary := asObj(payload["mesh"])
		lines = append(lines,
			fmt.Sprintf("组网：%s · transit %s ↔ landing %s", summary["interface"], summary["transit_ip"], summary["landing_ip"]),
			"流程：落地先建专用 WireGuard，再回传 endpoint；中转建同一小网并绑定 overlay 地址。",
		)
	} else {
		lines = append(lines, "流程：落地先回传 endpoint，Hub 再自动下发给中转绑定。")
	}
	if applyDataPlane && restartService {
		lines = append(lines, "生效：中转绑定后会自动 sing-box check、热重载，失败再重启。")
	} else if applyDataPlane {
		lines = append(lines, "生效：中转绑定后会自动 sing-box check，但不重启服务。")
	} else {
		lines = append(lines, "生效：仅写入配置，不自动 check/应用。")
	}
	return strings.Join(lines, "\n"), nil
}

func normalizeLinkMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "direct", "plain", "public", "直连":
		return "direct", nil
	case "mesh", "auto", "overlay", "wireguard", "wg", "auto-network", "自动组网", "组网":
		return "mesh", nil
	default:
		return "", fmt.Errorf("unsupported link mode %q; use direct or mesh", mode)
	}
}

func formatLinkMode(mode string) string {
	if mode == "mesh" {
		return "自动组网"
	}
	return "直连"
}

func makeMeshLinkPayload(transitID, landingID, endpointName, cidr, iface string, port int, endpoint string, keepalive int, configDir string, autoUp bool) (obj, error) {
	if _, err := ensureSafeName(transitID, "transit_id"); err != nil {
		return nil, err
	}
	if _, err := ensureSafeName(landingID, "landing_id"); err != nil {
		return nil, err
	}
	if _, err := ensureSafeName(endpointName, "endpoint_name"); err != nil {
		return nil, err
	}
	seed := transitID + "|" + landingID + "|" + endpointName
	if iface == "" {
		iface = meshInterfaceName(seed)
	}
	if _, err := ensureWGInterface(iface, "mesh_iface"); err != nil {
		return nil, err
	}
	if port == 0 {
		port = defaultMeshPort(seed)
	}
	if _, err := ensurePort(port, "mesh_port"); err != nil {
		return nil, err
	}
	if keepalive < 0 || keepalive > 3600 {
		return nil, errors.New("mesh_keepalive must be 0..3600")
	}
	if endpoint != "" {
		if err := validateHostPort(endpoint, "mesh_endpoint"); err != nil {
			return nil, err
		}
	}
	if configDir == "" {
		configDir = defaultMeshConfigDir
	}
	if !filepath.IsAbs(configDir) {
		return nil, errors.New("mesh_config_dir must be absolute")
	}
	networkCIDR, transitIP, landingIP, err := meshIPs(seed, cidr)
	if err != nil {
		return nil, err
	}
	transitPrivate, transitPublic, err := generateWireGuardKeypair()
	if err != nil {
		return nil, err
	}
	landingPrivate, landingPublic, err := generateWireGuardKeypair()
	if err != nil {
		return nil, err
	}
	psk, err := randomStdBase64(32)
	if err != nil {
		return nil, err
	}
	transit := obj{
		"role":                 "transit",
		"interface":            iface,
		"config_dir":           configDir,
		"address":              transitIP + "/32",
		"private_key":          transitPrivate,
		"peer_public_key":      landingPublic,
		"pre_shared_key":       psk,
		"peer_allowed_ips":     landingIP + "/32",
		"peer_endpoint":        endpoint,
		"peer_endpoint_port":   port,
		"persistent_keepalive": keepalive,
		"auto_up":              autoUp,
	}
	landing := obj{
		"role":             "landing",
		"interface":        iface,
		"config_dir":       configDir,
		"address":          landingIP + "/32",
		"listen_port":      port,
		"private_key":      landingPrivate,
		"peer_public_key":  transitPublic,
		"pre_shared_key":   psk,
		"peer_allowed_ips": transitIP + "/32",
		"auto_up":          autoUp,
	}
	return obj{
		"transit": transit,
		"landing": landing,
		"summary": obj{
			"mode":       "mesh",
			"interface":  iface,
			"cidr":       networkCIDR,
			"transit_ip": transitIP,
			"landing_ip": landingIP,
			"port":       port,
			"endpoint":   endpoint,
		},
	}, nil
}

func meshInterfaceName(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "rp" + hex.EncodeToString(sum[:4])
}

func defaultMeshPort(seed string) int {
	sum := sha256.Sum256([]byte(seed))
	return 20000 + ((int(sum[4])<<8)+int(sum[5]))%40000
}

func meshIPs(seed, cidr string) (string, string, string, error) {
	if strings.TrimSpace(cidr) == "" {
		sum := sha256.Sum256([]byte(seed))
		block := int(sum[7]&0x3f) * 4
		cidr = fmt.Sprintf("10.88.%d.%d/30", int(sum[6]), block)
	}
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid mesh cidr: %w", err)
	}
	base := ip.To4()
	if base == nil {
		return "", "", "", errors.New("mesh cidr must be IPv4 /30")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones != 30 {
		return "", "", "", errors.New("mesh cidr must be IPv4 /30")
	}
	base = network.IP.To4()
	transit := append(net.IP(nil), base...)
	landing := append(net.IP(nil), base...)
	transit[3]++
	landing[3] += 2
	return network.String(), transit.String(), landing.String(), nil
}

func generateWireGuardKeypair() (string, string, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(key.Bytes()), base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func randomStdBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func ensureWGInterface(value, field string) (string, error) {
	if !safeWGInterfaceRE.MatchString(value) {
		return "", fmt.Errorf("%s must match %s and be <=15 chars: %q", field, safeWGInterfaceRE.String(), value)
	}
	return value, nil
}

func ensureWireGuardMesh(spec obj) (obj, error) {
	if len(spec) == 0 {
		return nil, errors.New("mesh spec is required")
	}
	iface, err := ensureWGInterface(str(spec["interface"]), "mesh.interface")
	if err != nil {
		return nil, err
	}
	role := str(spec["role"])
	if role != "transit" && role != "landing" {
		return nil, errors.New("mesh.role must be transit or landing")
	}
	privateKey := strings.TrimSpace(str(spec["private_key"]))
	peerPublicKey := strings.TrimSpace(str(spec["peer_public_key"]))
	if privateKey == "" || peerPublicKey == "" {
		return nil, errors.New("mesh private_key and peer_public_key are required")
	}
	address := strings.TrimSpace(str(spec["address"]))
	if err := validateCIDRList(address, "mesh.address"); err != nil {
		return nil, err
	}
	peerAllowedIPs := strings.TrimSpace(firstNonEmpty(str(spec["peer_allowed_ips"]), str(spec["peer_allowed_ip"])))
	if err := validateCIDRList(peerAllowedIPs, "mesh.peer_allowed_ips"); err != nil {
		return nil, err
	}
	configDir := firstNonEmpty(str(spec["config_dir"]), defaultMeshConfigDir)
	if !filepath.IsAbs(configDir) {
		return nil, errors.New("mesh.config_dir must be absolute")
	}
	autoUp := true
	if v, ok := spec["auto_up"]; ok {
		autoUp = truthy(v)
	}
	listenPort := 0
	if int64Value(spec["listen_port"]) > 0 {
		listenPort, err = ensurePort(spec["listen_port"], "mesh.listen_port")
		if err != nil {
			return nil, err
		}
	}
	peerEndpoint := strings.TrimSpace(str(spec["peer_endpoint"]))
	if peerEndpoint == "" && str(spec["peer_endpoint_host"]) != "" {
		peerPort, err := ensurePort(firstNonNil(spec["peer_endpoint_port"], spec["listen_port"]), "mesh.peer_endpoint_port")
		if err != nil {
			return nil, err
		}
		peerEndpoint = net.JoinHostPort(str(spec["peer_endpoint_host"]), strconv.Itoa(peerPort))
	}
	if role == "transit" && peerEndpoint == "" {
		return nil, errors.New("mesh peer_endpoint is required for transit")
	}
	if peerEndpoint != "" {
		if err := validateHostPort(peerEndpoint, "mesh.peer_endpoint"); err != nil {
			return nil, err
		}
	}
	keepalive := int(int64Value(spec["persistent_keepalive"]))
	if keepalive < 0 || keepalive > 3600 {
		return nil, errors.New("mesh.persistent_keepalive must be 0..3600")
	}
	confText := renderWireGuardConfig(obj{
		"private_key":          privateKey,
		"address":              address,
		"listen_port":          listenPort,
		"peer_public_key":      peerPublicKey,
		"pre_shared_key":       str(spec["pre_shared_key"]),
		"peer_allowed_ips":     peerAllowedIPs,
		"peer_endpoint":        peerEndpoint,
		"persistent_keepalive": keepalive,
	})
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}
	configPath := filepath.Join(configDir, iface+".conf")
	if existing, err := os.ReadFile(configPath); err == nil && len(existing) > 0 && !bytes.Contains(existing, []byte(wireGuardConfigMarker)) {
		return nil, fmt.Errorf("refusing to overwrite non-RelayPilot WireGuard config: %s", configPath)
	}
	if err := os.WriteFile(configPath, []byte(confText), 0o600); err != nil {
		return nil, err
	}
	upResult := "not_started"
	if autoUp {
		if _, err := exec.LookPath("wg-quick"); err != nil {
			return nil, fmt.Errorf("wg-quick is required for mesh mode; config written to %s", configPath)
		}
		_ = runCommand(15*time.Second, "wg-quick", "down", configPath)
		if err := runCommand(30*time.Second, "wg-quick", "up", configPath); err != nil {
			return nil, fmt.Errorf("wg-quick up failed for %s: %w", iface, err)
		}
		upResult = "up"
	}
	return obj{
		"role":             role,
		"interface":        iface,
		"config":           configPath,
		"address":          address,
		"peer_allowed_ips": peerAllowedIPs,
		"peer_endpoint":    peerEndpoint,
		"auto_up":          autoUp,
		"state":            upResult,
	}, nil
}

func validateHostPort(value, field string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", field, err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s host is empty", field)
	}
	if _, err := ensurePort(port, field+".port"); err != nil {
		return err
	}
	return nil
}

func validateCIDRList(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("%s contains an empty CIDR", field)
		}
		if _, _, err := net.ParseCIDR(part); err != nil {
			return fmt.Errorf("%s must contain CIDR values: %w", field, err)
		}
	}
	return nil
}

func renderWireGuardConfig(spec obj) string {
	var b strings.Builder
	b.WriteString(wireGuardConfigMarker + "\n")
	b.WriteString("# Safe to update via RelayPilot; remove manually only after wg-quick down.\n\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", strings.TrimSpace(str(spec["private_key"])))
	fmt.Fprintf(&b, "Address = %s\n", strings.TrimSpace(str(spec["address"])))
	if port := int64Value(spec["listen_port"]); port > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", port)
	}
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", strings.TrimSpace(str(spec["peer_public_key"])))
	if psk := strings.TrimSpace(str(spec["pre_shared_key"])); psk != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", psk)
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.TrimSpace(str(spec["peer_allowed_ips"])))
	if endpoint := strings.TrimSpace(str(spec["peer_endpoint"])); endpoint != "" {
		fmt.Fprintf(&b, "Endpoint = %s\n", endpoint)
	}
	if keepalive := int64Value(spec["persistent_keepalive"]); keepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", keepalive)
	}
	return b.String()
}
