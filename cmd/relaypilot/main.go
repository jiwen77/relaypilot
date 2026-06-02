package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	endpointKind          = "relaypilot/landing-endpoint"
	agentRegistrationKind = "relaypilot/agent-registration"
	hubTaskKind           = "relaypilot/hub-task"
	version               = 1

	telegramConfigName             = "telegram.json"
	hubAgentsName                  = "hub-agents.json"
	hubRemovedAgentsName           = "hub-removed-agents.json"
	hubAlertsName                  = "hub-alerts.json"
	hubTasksDirName                = "hub-tasks"
	hubAgentTokensName             = "hub-agent-tokens.json"
	hubAuthNoncesName              = "hub-auth-nonces.json"
	hubEnrollCodesName             = "hub-enroll-codes.json"
	hubTLSDirName                  = "hub-tls"
	hubTLSCACertName               = "ca.crt"
	hubTLSCAKeyName                = "ca.key"
	hubTLSServerCertName           = "hub.crt"
	hubTLSServerKeyName            = "hub.key"
	agentEnrollmentName            = "agent-enrollment.json"
	defaultStateDir                = "/etc/relaypilot"
	defaultConfDir                 = "/etc/sing-box/conf"
	defaultHubTLSDays              = 3650
	defaultAgentCertDays           = 1095
	defaultEnrollCodeTTLSeconds    = 600
	agentAuthSkewSeconds           = 300
	heartbeatStaleSeconds          = 120
	heartbeatOfflineSeconds        = 600
	offlineAlertSeconds            = 86400
	offlineAlertSnoozeSecs         = 86400
	defaultTopologyInterval        = 300
	maxJSONFileBytes               = 2 << 20
	maxHTTPBodyBytes               = 4 << 20
	maxTelegramTextBytes           = 3900
	maxHubTasksPerPoll             = 20
	maxConfigFiles                 = 64
	maxPendingTGBatches            = 32
	tgBatchResultTimeoutSec        = 120
	defaultTaskLeaseTimeoutSeconds = 120
	defaultTaskMaxLeaseCount       = 3
	defaultMeshConfigDir           = "/etc/wireguard"
	defaultMeshKeepaliveSeconds    = 25
	wireGuardConfigMarker          = "# RelayPilot managed WireGuard mesh"
)

var buildVersion = "dev"

var safeNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
var safeTagRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}$`)
var safeWGInterfaceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

var ss2022KeyLengths = map[string]int{
	"2022-blake3-aes-128-gcm":       16,
	"2022-blake3-aes-256-gcm":       32,
	"2022-blake3-chacha20-poly1305": 32,
}

var ssMethods = map[string]bool{
	"none":                          true,
	"aes-128-gcm":                   true,
	"aes-192-gcm":                   true,
	"aes-256-gcm":                   true,
	"chacha20-ietf-poly1305":        true,
	"xchacha20-ietf-poly1305":       true,
	"aes-128-ctr":                   true,
	"aes-192-ctr":                   true,
	"aes-256-ctr":                   true,
	"aes-128-cfb":                   true,
	"aes-192-cfb":                   true,
	"aes-256-cfb":                   true,
	"rc4-md5":                       true,
	"chacha20-ietf":                 true,
	"xchacha20":                     true,
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
}

var telegramCommands = []obj{
	{"command": "relaypilot_help", "args": "", "description": "Show available RelayPilot commands", "scope": "read"},
	{"command": "relaypilot_status", "args": "", "description": "Show endpoint and sing-box config summary", "scope": "read"},
	{"command": "relaypilot_doctor", "args": "", "description": "Show local agent path/config health", "scope": "read"},
	{"command": "relaypilot_endpoints", "args": "", "description": "List imported landing endpoints without secrets", "scope": "read"},
	{"command": "relaypilot_show_endpoint", "args": "<name>", "description": "Show one endpoint summary without password", "scope": "read"},
	{"command": "relaypilot_inspect_conf", "args": "[path]", "description": "Inspect sing-box config summary", "scope": "read"},
}

var hubTelegramCommandRows = []obj{
	{"command": "relaypilot_panel", "args": "", "description": "Open Hub control panel", "scope": "read"},
	{"command": "relaypilot_help", "args": "", "description": "Show hub command syntax", "scope": "read"},
	{"command": "relaypilot_agents", "args": "", "description": "List registered transit and landing agents", "scope": "read"},
	{"command": "relaypilot_topology", "args": "", "description": "Show transit to landing tree view", "scope": "read"},
	{"command": "relaypilot_status", "args": "[hub|all|transit|landing|agent_id]", "description": "Show hub status or queue status checks for selected agents", "scope": "read"},
	{"command": "relaypilot_doctor", "args": "[hub|all|agent_id]", "description": "Show hub health or queue agent doctor checks", "scope": "read"},
	{"command": "relaypilot_endpoints", "args": "[all|transit|landing|agent_id]", "description": "Queue endpoint listing for selected agents", "scope": "read"},
	{"command": "relaypilot_show_endpoint", "args": "<agent_id> <endpoint_name>", "description": "Queue endpoint detail request for one agent", "scope": "read"},
	{"command": "relaypilot_inspect_conf", "args": "<agent_id> [path]", "description": "Queue config inspection for one agent", "scope": "read"},
	{"command": "relaypilot_link", "args": "<transit_id> <landing_id> [auth_user] [endpoint_name] [--mode direct|mesh]", "description": "Link one transit to one landing endpoint", "scope": "write"},
	{"command": "relaypilot_update", "args": "<hub|all|transit|landing|agent_id> <version|latest> [--restart]", "description": "Update Hub or queue node self-update tasks", "scope": "write"},
	{"command": "relaypilot_tasks", "args": "", "description": "List queued hub tasks", "scope": "read"},
	{"command": "relaypilot_results", "args": "[batch_id]", "description": "Show completed hub task results", "scope": "read"},
	{"command": "relaypilot_alerts", "args": "", "description": "List offline-node alerts", "scope": "read"},
}

type obj = map[string]any

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage()
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Println("RelayPilot Go core " + buildVersion)
		return nil
	case "migrate-state":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		fromDir := fs.String("from", "", "source state directory")
		toDir := fs.String("to", defaultStateDir, "RelayPilot state directory")
		force := fs.Bool("force", false, "overwrite conflicting files")
		dryRun := fs.Bool("dry-run", false, "show migration plan without copying")
		_ = fs.Parse(args[1:])
		res, err := migrateStateDir(*fromDir, *toDir, *force, *dryRun)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "tg-config":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		botToken := fs.String("bot-token", "", "Telegram bot token")
		chatID := fs.String("chat-id", "", "Telegram chat id")
		apiBase := fs.String("api-base", "https://api.telegram.org", "Telegram API base")
		disabled := fs.Bool("disabled", false, "write config as disabled")
		_ = fs.Parse(args[1:])
		path, err := configureTelegram(*stateDir, *botToken, *chatID, *apiBase, !*disabled)
		if err != nil {
			return err
		}
		return printJSON(obj{"telegram_config": path})
	case "tg-status":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		_ = fs.Parse(args[1:])
		return printJSON(telegramStatus(*stateDir))
	case "tg-commands":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		botFather := fs.Bool("botfather", false, "print BotFather format")
		hub := fs.Bool("hub", false, "use Hub command set")
		_ = fs.Parse(args[1:])
		if *asJSON {
			return printJSON(obj{"commands": selectedTelegramCommands(*hub)})
		}
		fmt.Println(formatTelegramCommands(*botFather, *hub))
		return nil
	case "tg-register-commands":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		scope := fs.String("scope", "", "Telegram Bot API command scope")
		languageCode := fs.String("language-code", "", "Telegram command language code")
		hub := fs.Bool("hub", false, "use Hub command set")
		dryRun := fs.Bool("dry-run", false, "do not call Telegram API")
		_ = fs.Parse(args[1:])
		res, err := registerTelegramCommands(*stateDir, *scope, *languageCode, *hub, *dryRun)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "tg-get-commands":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		scope := fs.String("scope", "", "Telegram Bot API command scope")
		languageCode := fs.String("language-code", "", "Telegram command language code")
		dryRun := fs.Bool("dry-run", false, "do not call Telegram API")
		_ = fs.Parse(args[1:])
		res, err := remoteTelegramCommands(*stateDir, "getMyCommands", *scope, *languageCode, *dryRun)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "tg-delete-commands":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		scope := fs.String("scope", "", "Telegram Bot API command scope")
		languageCode := fs.String("language-code", "", "Telegram command language code")
		dryRun := fs.Bool("dry-run", false, "do not call Telegram API")
		_ = fs.Parse(args[1:])
		res, err := remoteTelegramCommands(*stateDir, "deleteMyCommands", *scope, *languageCode, *dryRun)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "tg-send":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		text := fs.String("text", "", "message text")
		dryRun := fs.Bool("dry-run", false, "do not call Telegram API")
		parseMode := fs.String("parse-mode", "", "Telegram parse_mode")
		_ = fs.Parse(args[1:])
		res, err := sendTelegramMessageCLI(*stateDir, *text, *parseMode, *dryRun)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "tg-dispatch":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		text := fs.String("text", "", "Telegram command text")
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		hub := fs.Bool("hub", false, "dispatch through Hub")
		hubStateDir := fs.String("hub-state-dir", "", "Hub state dir")
		_ = fs.Parse(args[1:])
		if *hub {
			targetState := *hubStateDir
			if targetState == "" {
				targetState = *stateDir
			}
			out, err := hubDispatchCommand(targetState, *text)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		}
		out, err := telegramDispatch(*text, *stateDir, *conf)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	case "generate-ss-password":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		method := fs.String("method", "2022-blake3-aes-128-gcm", "Shadowsocks method")
		_ = fs.Parse(args[1:])
		password, err := generateSSPassword(*method)
		if err != nil {
			return err
		}
		fmt.Println(password)
		return nil
	case "render-landing-ss":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		name := fs.String("name", "", "endpoint name")
		server := fs.String("server", "", "transit-visible server")
		listen := fs.String("listen", "::", "listen address")
		listenPort := fs.Int("listen-port", 0, "listen port")
		serverPort := fs.Int("server-port", 0, "server port")
		method := fs.String("method", "2022-blake3-aes-128-gcm", "Shadowsocks method")
		password := fs.String("password", "", "Shadowsocks password")
		network := fs.String("network", "tcp,udp", "network")
		inboundTag := fs.String("inbound-tag", "ss-in", "inbound tag")
		endpointTag := fs.String("endpoint-tag", "", "endpoint/outbound tag")
		configOutput := fs.String("config-output", "", "config output path")
		endpointOutput := fs.String("endpoint-output", "", "endpoint output path")
		_ = fs.Parse(args[1:])
		if *password == "" {
			generated, err := generateSSPassword(*method)
			if err != nil {
				return err
			}
			*password = generated
		}
		if *serverPort == 0 {
			*serverPort = *listenPort
		}
		endpoint, config, err := renderLandingSS(*name, *server, *listen, *listenPort, *serverPort, *method, *password, *network, *inboundTag, *endpointTag)
		if err != nil {
			return err
		}
		if *configOutput == "" || *endpointOutput == "" {
			return errors.New("--config-output and --endpoint-output are required")
		}
		if err := writeJSON(*configOutput, config, 0o644); err != nil {
			return err
		}
		if err := writeJSON(*endpointOutput, endpoint, 0o600); err != nil {
			return err
		}
		return printJSON(obj{"config": *configOutput, "endpoint": *endpointOutput, "endpoint_name": endpoint["name"], "endpoint_tag": endpoint["tag"]})
	case "ensure-transit-reality":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		listen := fs.String("listen", "::", "listen address")
		listenPort := fs.Int("listen-port", 443, "listen port")
		inboundTag := fs.String("inbound-tag", "vless-in", "VLESS inbound tag")
		serverName := fs.String("server-name", "www.cloudflare.com", "Reality client/server_name")
		handshakeServer := fs.String("handshake-server", "", "Reality handshake server; defaults to --server-name")
		handshakePort := fs.Int("handshake-port", 443, "Reality handshake server port")
		privateKey := fs.String("private-key", "", "Reality private key; empty preserves existing or generates one")
		shortID := fs.String("short-id", "", "Reality short_id hex; empty preserves existing or generates one")
		maxTimeDifference := fs.String("max-time-difference", "1m", "Reality max_time_difference")
		noBackup := fs.Bool("no-backup", false, "do not backup existing config")
		_ = fs.Parse(args[1:])
		if *handshakeServer == "" {
			*handshakeServer = *serverName
		}
		summary, err := ensureTransitReality(*conf, *stateDir, *listen, *listenPort, *inboundTag, *serverName, *handshakeServer, *handshakePort, *privateKey, *shortID, *maxTimeDifference, !*noBackup)
		if err != nil {
			return err
		}
		return printJSON(summary)
	case "validate-endpoint":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: validate-endpoint <endpoint.json>")
		}
		endpoint, err := loadEndpoint(fs.Arg(0), true)
		if err != nil {
			return err
		}
		return printJSON(endpoint)
	case "render-outbound":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: render-outbound <endpoint.json>")
		}
		endpoint, err := loadEndpoint(fs.Arg(0), true)
		if err != nil {
			return err
		}
		outbound, err := endpointToOutbound(endpoint)
		if err != nil {
			return err
		}
		return printJSON(outbound)
	case "import-endpoint":
		stateDir, endpointArg, err := parseStateDirAndOneArg(args[1:], "usage: import-endpoint <endpoint.json>")
		if err != nil {
			return err
		}
		dest, err := importEndpoint(endpointArg, stateDir)
		if err != nil {
			return err
		}
		return printJSON(obj{"imported": dest})
	case "export-endpoint":
		stateDir, nameArg, err := parseStateDirAndOneArg(args[1:], "usage: export-endpoint <name>")
		if err != nil {
			return err
		}
		endpoint, err := exportEndpoint(stateDir, nameArg)
		if err != nil {
			return err
		}
		return printJSON(endpoint)
	case "bind-transit":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		endpointPath := fs.String("endpoint", "", "endpoint path")
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		inboundTag := fs.String("inbound-tag", "", "VLESS inbound tag")
		authUser := fs.String("auth-user", "", "auth user")
		clientUUID := fs.String("uuid", "", "client UUID")
		flow := fs.String("flow", "xtls-rprx-vision", "VLESS flow")
		noFinalDirect := fs.Bool("no-final-direct", false, "do not set route final direct")
		noBackup := fs.Bool("no-backup", false, "do not backup config")
		_ = fs.Parse(args[1:])
		if *endpointPath == "" {
			return errors.New("--endpoint is required")
		}
		endpoint, err := loadEndpoint(*endpointPath, true)
		if err != nil {
			return err
		}
		if *authUser == "" {
			*authUser = str(endpoint["name"])
		}
		if *clientUUID == "" {
			*clientUUID = generateUUID()
		}
		summary, err := bindTransit(*conf, endpoint, *inboundTag, *authUser, *clientUUID, *flow, !*noFinalDirect, *stateDir, !*noBackup)
		if err != nil {
			return err
		}
		return printJSON(summary)
	case "list-endpoints":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		items, err := listEndpoints(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "endpoints": items})
		}
		if len(items) == 0 {
			fmt.Println("(no endpoints)")
			return nil
		}
		for _, item := range items {
			fmt.Printf("%s\t%s\t%s\t%s:%v\t%s\n", str(item["name"]), str(item["protocol"]), str(item["tag"]), str(item["server"]), item["server_port"], str(item["path"]))
		}
		return nil
	case "inspect-conf":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		summary, err := inspectConfig(*conf)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(summary)
		}
		fmt.Print(formatConfigText(summary))
		return nil
	case "hub-import-agent":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: hub-import-agent <registration.json>")
		}
		agent, err := importAgentRegistration(*stateDir, fs.Arg(0))
		if err != nil {
			return err
		}
		return printJSON(obj{"imported": agent["id"], "role": agent["role"]})
	case "hub-agent-export":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		agentID := fs.String("agent-id", "", "agent id")
		role := fs.String("role", "", "agent role")
		name := fs.String("name", "", "display name")
		endpoint := fs.String("endpoint", "", "agent endpoint")
		labels := fs.String("labels", "", "comma-separated key=value labels")
		transport := fs.String("transport", "poll", "transport")
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		conf := fs.String("conf", "", "config path for topology snapshot")
		noSnapshot := fs.Bool("no-snapshot", false, "disable topology snapshot")
		output := fs.String("output", "", "output path")
		_ = fs.Parse(args[1:])
		registration, err := makeAgentRegistration(*agentID, *role, *name, *endpoint, *labels, *transport, *stateDir, *conf, *noSnapshot)
		if err != nil {
			return err
		}
		if *output != "" {
			if err := writeJSON(*output, registration, 0o600); err != nil {
				return err
			}
			return printJSON(obj{"registration": *output, "agent_id": registration["id"]})
		}
		return printJSON(registration)
	case "hub-agents":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		agents, err := listHubAgents(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "agents": agents})
		}
		if len(agents) == 0 {
			fmt.Println("(no agents)")
			return nil
		}
		for _, agent := range agents {
			fmt.Printf("%s\t%s\t%s\t%s\n", str(agent["id"]), str(agent["role"]), str(agent["name"]), str(agent["transport"]))
		}
		return nil
	case "hub-remove-agent":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		reason := fs.String("reason", "removed", "removal reason")
		noTombstone := fs.Bool("no-tombstone", false, "do not write tombstone")
		keepTasks := fs.Bool("keep-tasks", false, "do not cancel queued/running tasks")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: hub-remove-agent <agent_id>")
		}
		res, err := removeHubAgent(*stateDir, fs.Arg(0), *reason, !*noTombstone, !*keepTasks)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-removed-agents":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		agents, err := listRemovedAgents(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "agents": agents})
		}
		if len(agents) == 0 {
			fmt.Println("(no removed agents)")
			return nil
		}
		for _, item := range agents {
			agent := asObj(item["agent"])
			fmt.Printf("%s\t%s\t%s\t%s\t%v\n", str(item["id"]), str(agent["role"]), str(agent["name"]), str(item["reason"]), item["removed_at"])
		}
		return nil
	case "hub-issue-token":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		token := fs.String("token", "", "explicit token")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: hub-issue-token <agent_id>")
		}
		res, err := issueHubAgentToken(*stateDir, fs.Arg(0), *token)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-init-tls":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		hosts := fs.String("hosts", "", "comma-separated DNS names or IPs for the Hub certificate")
		host := fs.String("host", "", "single DNS name or IP for the Hub certificate")
		days := fs.Int("days", defaultHubTLSDays, "certificate validity in days")
		force := fs.Bool("force", false, "overwrite existing TLS files")
		_ = fs.Parse(args[1:])
		res, err := initHubTLS(*stateDir, hubTLSInitOptions{Hosts: append(splitCSV(*hosts), splitCSV(*host)...), Days: *days, Force: *force})
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-issue-agent-cert":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		outputDir := fs.String("output-dir", "", "output directory for agent certificate/key")
		days := fs.Int("days", defaultAgentCertDays, "certificate validity in days")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: hub-issue-agent-cert [--state-dir DIR] [--output-dir DIR] AGENT_ID")
		}
		res, err := issueHubAgentClientCert(*stateDir, fs.Arg(0), *outputDir, *days)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-provision-agent":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		hubURL := fs.String("hub-url", "", "public Hub URL used by the agent, e.g. https://hub.example:8443")
		agentID := fs.String("agent-id", "", "agent id")
		role := fs.String("role", "transit", "agent role")
		name := fs.String("name", "", "display name")
		labels := fs.String("labels", "", "comma-separated key=value labels")
		days := fs.Int("days", defaultAgentCertDays, "certificate validity in days")
		_ = fs.Parse(args[1:])
		res, err := provisionHubAgentBundle(*stateDir, hubAgentProvisionOptions{HubURL: *hubURL, AgentID: *agentID, Role: *role, Name: *name, Labels: *labels, Days: *days})
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-create-enroll-code", "hub-enroll-code":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		hubURL := fs.String("hub-url", "", "public Hub URL used by the agent, e.g. https://hub.example:8443")
		publicHost := fs.String("public-host", "", "public Hub IP or DNS name; defaults to detected public IP")
		publicPort := fs.Int("port", 8443, "public Hub HTTPS port used when --hub-url is omitted")
		agentID := fs.String("agent-id", "", "agent id")
		role := fs.String("role", "transit", "agent role")
		name := fs.String("name", "", "display name")
		labels := fs.String("labels", "", "comma-separated key=value labels")
		ttl := fs.Duration("ttl", time.Duration(defaultEnrollCodeTTLSeconds)*time.Second, "enrollment code lifetime")
		days := fs.Int("days", defaultAgentCertDays, "client certificate validity in days")
		_ = fs.Parse(args[1:])
		resolvedHubURL, err := resolveHubPublicURL(hubPublicURLOptions{ExplicitURL: *hubURL, PublicHost: *publicHost, Port: *publicPort}, detectPublicIP)
		if err != nil {
			return err
		}
		res, err := createHubEnrollInvite(*stateDir, hubEnrollCodeOptions{HubURL: resolvedHubURL, AgentID: *agentID, Role: *role, Name: *name, Labels: *labels, TTLSeconds: int64(ttl.Seconds()), Days: *days})
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-rotate-token":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		token := fs.String("token", "", "explicit token")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: hub-rotate-token <agent_id>")
		}
		res, err := rotateHubAgentToken(*stateDir, fs.Arg(0), *token)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-revoke-token":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: hub-revoke-token <agent_id>")
		}
		res, err := revokeHubAgentToken(*stateDir, fs.Arg(0))
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-tokens":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		tokens, err := listHubAgentTokens(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "tokens": tokens})
		}
		fmt.Println(formatHubAgentTokensText(tokens))
		return nil
	case "hub-dispatch":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		text := fs.String("text", "", "hub command text")
		_ = fs.Parse(args[1:])
		if *text == "" && fs.NArg() > 0 {
			*text = strings.Join(fs.Args(), " ")
		}
		out, err := hubDispatchCommand(*stateDir, *text)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	case "hub-recover-tasks":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		leaseTimeout := fs.Int64("lease-timeout-seconds", defaultTaskLeaseTimeoutSeconds, "seconds before a running task lease is stale")
		maxLeaseCount := fs.Int64("max-lease-count", defaultTaskMaxLeaseCount, "fail a task after this many timed-out leases")
		_ = fs.Parse(args[1:])
		res, err := recoverStaleHubTasks(*stateDir, *leaseTimeout, *maxLeaseCount)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-tasks":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		tasks, err := listHubTasks(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "tasks": tasks})
		}
		fmt.Println(formatHubTasksText(tasks))
		return nil
	case "hub-results":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		batchID := fs.String("batch-id", "", "batch id")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		tasks, err := listHubTasks(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "batch_id": *batchID, "tasks": filterDoneTasks(tasks, *batchID)})
		}
		fmt.Println(formatHubTaskResultsText(tasks, *batchID))
		return nil
	case "hub-alert-offline":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		thresholdSeconds := fs.Int64("threshold-seconds", offlineAlertSeconds, "offline threshold seconds")
		snoozeSeconds := fs.Int64("snooze-seconds", offlineAlertSnoozeSecs, "alert snooze seconds")
		dryRun := fs.Bool("dry-run", false, "do not call Telegram or persist new alerts")
		_ = fs.Parse(args[1:])
		res, err := scanOfflineAgentAlerts(*stateDir, *thresholdSeconds, *snoozeSeconds, *dryRun)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-alerts":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		alerts, err := listHubAlerts(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "alerts": alerts})
		}
		fmt.Println(formatHubAlertsText(alerts))
		return nil
	case "hub-alert-callback":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		data := fs.String("data", "", "Telegram callback_data")
		snoozeSeconds := fs.Int64("snooze-seconds", offlineAlertSnoozeSecs, "alert snooze seconds")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		if *data == "" && fs.NArg() > 0 {
			*data = fs.Arg(0)
		}
		if *data == "" {
			return errors.New("--data is required")
		}
		res, err := handleHubAlertCallback(*stateDir, *data, *snoozeSeconds)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(res)
		}
		fmt.Println(str(res["message"]))
		return nil
	case "hub-daemon":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		host := fs.String("host", "127.0.0.1", "listen host")
		port := fs.Int("port", 8080, "listen port")
		tlsCert := fs.String("tls-cert", "", "Hub TLS certificate path")
		tlsKey := fs.String("tls-key", "", "Hub TLS private key path")
		clientCA := fs.String("client-ca", "", "CA certificate used to verify agent client certificates")
		requireClientCert := fs.Bool("require-client-cert", false, "require and verify agent client certificates")
		quiet := fs.Bool("quiet", false, "quiet logs")
		_ = fs.Parse(args[1:])
		return serveHub(*stateDir, *host, *port, *quiet, hubListenOptions{TLSCertPath: *tlsCert, TLSKeyPath: *tlsKey, ClientCAPath: *clientCA, RequireClientCert: *requireClientCert})
	case "bot-daemon":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		interval := fs.Int("interval", 2, "retry interval seconds after empty/error polls")
		timeout := fs.Int("timeout", 25, "Telegram long-poll timeout seconds")
		limit := fs.Int("limit", 20, "max updates per request")
		quiet := fs.Bool("quiet", false, "quiet logs")
		_ = fs.Parse(args[1:])
		return runTelegramHubDaemon(*stateDir, *interval, *timeout, *limit, *quiet)
	case "agent-poll-once":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		hubURL := fs.String("hub-url", "", "hub URL")
		agentID := fs.String("agent-id", "", "agent id")
		role := fs.String("role", "", "agent role")
		name := fs.String("name", "", "display name")
		labels := fs.String("labels", "", "comma-separated key=value labels")
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		enrollmentFile := fs.String("enrollment-file", "", "agent enrollment file generated by agent-enroll")
		token := fs.String("token", "", "agent token")
		tokenFile := fs.String("token-file", "", "agent token file")
		caCert := fs.String("ca-cert", "", "CA certificate for HTTPS Hub verification")
		clientCert := fs.String("client-cert", "", "agent client certificate for mTLS")
		clientKey := fs.String("client-key", "", "agent client private key for mTLS")
		tlsServerName := fs.String("tls-server-name", "", "override TLS server name")
		maxTasks := fs.Int("max-tasks", 5, "max tasks")
		topologyInterval := fs.Int("topology-interval", 0, "reuse topology snapshot for this many seconds; 0 disables cache")
		timeout := fs.Int("timeout", 10, "HTTP timeout seconds")
		_ = fs.Parse(args[1:])
		_ = *name
		if _, err := parseLabels(*labels); err != nil {
			return err
		}
		if err := applyAgentEnrollmentDefaults(*enrollmentFile, hubURL, agentID, role, tokenFile, caCert, clientCert, clientKey); err != nil {
			return err
		}
		if *role == "" {
			*role = "transit"
		}
		secret, err := readAgentToken(*token, *tokenFile)
		if err != nil {
			return err
		}
		tlsConfig, err := loadAgentTLSConfig(*caCert, *clientCert, *clientKey, *tlsServerName)
		if err != nil {
			return err
		}
		poller := newAgentPoller(*hubURL, *agentID, secret, *role, *stateDir, *conf, *maxTasks, time.Duration(*timeout)*time.Second, *topologyInterval)
		poller.tlsConfig = tlsConfig
		res, err := poller.pollOnce()
		if err != nil {
			return err
		}
		return printJSON(res)
	case "agent-poll-loop":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		hubURL := fs.String("hub-url", "", "hub URL")
		agentID := fs.String("agent-id", "", "agent id")
		role := fs.String("role", "", "agent role")
		name := fs.String("name", "", "display name")
		labels := fs.String("labels", "", "comma-separated key=value labels")
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		enrollmentFile := fs.String("enrollment-file", "", "agent enrollment file generated by agent-enroll")
		token := fs.String("token", "", "agent token")
		tokenFile := fs.String("token-file", "", "agent token file")
		caCert := fs.String("ca-cert", "", "CA certificate for HTTPS Hub verification")
		clientCert := fs.String("client-cert", "", "agent client certificate for mTLS")
		clientKey := fs.String("client-key", "", "agent client private key for mTLS")
		tlsServerName := fs.String("tls-server-name", "", "override TLS server name")
		interval := fs.Int("interval", 30, "poll interval seconds")
		maxTasks := fs.Int("max-tasks", 5, "max tasks")
		topologyInterval := fs.Int("topology-interval", defaultTopologyInterval, "reuse topology snapshot for this many seconds")
		timeout := fs.Int("timeout", 10, "HTTP timeout seconds")
		_ = fs.Parse(args[1:])
		_ = *name
		if _, err := parseLabels(*labels); err != nil {
			return err
		}
		if err := applyAgentEnrollmentDefaults(*enrollmentFile, hubURL, agentID, role, tokenFile, caCert, clientCert, clientKey); err != nil {
			return err
		}
		if *role == "" {
			*role = "transit"
		}
		secret, err := readAgentToken(*token, *tokenFile)
		if err != nil {
			return err
		}
		tlsConfig, err := loadAgentTLSConfig(*caCert, *clientCert, *clientKey, *tlsServerName)
		if err != nil {
			return err
		}
		return agentPollLoopWithTLS(*hubURL, *agentID, secret, *role, *stateDir, *conf, *interval, *maxTasks, time.Duration(*timeout)*time.Second, *topologyInterval, tlsConfig)
	case "agent-enroll":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		bundle := fs.String("bundle", "", "base64 enrollment bundle from hub-provision-agent")
		bundleFile := fs.String("bundle-file", "", "file containing enrollment bundle")
		invite := fs.String("invite", "", "base64 invite from hub-create-enroll-code")
		enroll := fs.String("enroll", "", "alias for --invite")
		inviteFile := fs.String("invite-file", "", "file containing enrollment invite")
		hubURL := fs.String("hub-url", "", "Hub URL for --code enrollment")
		code := fs.String("code", "", "short-lived enrollment code from Hub")
		agentID := fs.String("agent-id", "", "agent id for --code enrollment")
		role := fs.String("role", "", "agent role for --code enrollment; defaults to Hub invite binding")
		name := fs.String("name", "", "display name for --code enrollment")
		labels := fs.String("labels", "", "comma-separated key=value labels for --code enrollment")
		caCert := fs.String("ca-cert", "", "CA certificate for HTTPS Hub verification during --code enrollment")
		tlsServerName := fs.String("tls-server-name", "", "override TLS server name during --code enrollment")
		pollOnce := fs.Bool("poll-once", false, "run one heartbeat poll after writing files")
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		timeout := fs.Int("timeout", 10, "HTTP timeout seconds")
		_ = fs.Parse(args[1:])
		if *bundle == "" && *bundleFile != "" {
			data, err := os.ReadFile(*bundleFile)
			if err != nil {
				return err
			}
			*bundle = strings.TrimSpace(string(data))
		}
		if *invite == "" && *enroll != "" {
			*invite = *enroll
		}
		if *invite == "" && *inviteFile != "" {
			data, err := os.ReadFile(*inviteFile)
			if err != nil {
				return err
			}
			*invite = strings.TrimSpace(string(data))
		}
		opts := agentEnrollOptions{
			StateDir:      *stateDir,
			HubURL:        *hubURL,
			Code:          *code,
			AgentID:       *agentID,
			Role:          *role,
			Name:          *name,
			Labels:        *labels,
			CACertPath:    *caCert,
			TLSServerName: *tlsServerName,
		}
		var res obj
		var err error
		switch {
		case *bundle != "":
			res, err = agentEnrollBundle(*bundle, opts)
		case *invite != "":
			res, err = agentEnrollInvite(*invite, opts, time.Duration(*timeout)*time.Second)
		case *code != "":
			res, err = agentEnrollCode(opts, time.Duration(*timeout)*time.Second)
		default:
			err = errors.New("agent-enroll requires --invite/--enroll, --code, or --bundle")
		}
		if err != nil {
			return err
		}
		if *pollOnce {
			cfg, err := loadAgentEnrollment(*stateDir)
			if err != nil {
				return err
			}
			tlsConfig, err := loadAgentTLSConfig(cfg.CACertPath, cfg.ClientCertPath, cfg.ClientKeyPath, "")
			if err != nil {
				return err
			}
			tokenData, err := os.ReadFile(cfg.TokenFile)
			if err != nil {
				return err
			}
			pollRes, err := agentPollOnceWithTLS(cfg.HubURL, cfg.AgentID, strings.TrimSpace(string(tokenData)), cfg.Role, *stateDir, *conf, 0, 10*time.Second, tlsConfig)
			if err != nil {
				return err
			}
			res["poll"] = pollRes
		}
		return printJSON(res)
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage() {
	fmt.Print(`RelayPilot Go core

Commands:
  generate-ss-password [--method METHOD]
  migrate-state --from DIR [--to /etc/relaypilot] [--dry-run] [--force]
  tg-config --state-dir DIR --bot-token TOKEN --chat-id ID [--api-base URL]
  tg-status [--state-dir DIR]
  tg-commands [--hub] [--json] [--botfather]
  tg-register-commands [--state-dir DIR] [--hub] [--dry-run]
  tg-get-commands [--state-dir DIR] [--dry-run]
  tg-delete-commands [--state-dir DIR] [--dry-run]
  tg-dispatch --text "/status" [--state-dir DIR] [--hub]
  tg-send --text TEXT [--state-dir DIR] [--dry-run]
  render-landing-ss --name NAME --server HOST --listen-port PORT --config-output PATH --endpoint-output PATH
  validate-endpoint ENDPOINT_JSON
  render-outbound ENDPOINT_JSON
  import-endpoint [--state-dir DIR] ENDPOINT_JSON
  export-endpoint [--state-dir DIR] NAME
  bind-transit --conf PATH --endpoint ENDPOINT_JSON [--auth-user USER]
  list-endpoints [--state-dir DIR] [--json]
  inspect-conf [--conf PATH] [--json]
  hub-agent-export --agent-id ID --role transit|landing [--output PATH]
  hub-import-agent [--state-dir DIR] REGISTRATION_JSON
  hub-agents [--state-dir DIR] [--json]
  hub-remove-agent [--state-dir DIR] AGENT_ID [--reason REASON]
  hub-removed-agents [--state-dir DIR] [--json]
  hub-issue-token [--state-dir DIR] AGENT_ID
  hub-init-tls [--state-dir DIR] --host HUB_IP_OR_DNS [--force]
  hub-issue-agent-cert [--state-dir DIR] [--output-dir DIR] AGENT_ID
  hub-provision-agent [--state-dir DIR] --hub-url https://HUB:8443 --agent-id ID --role transit|landing
  hub-create-enroll-code [--state-dir DIR] [--hub-url https://HUB:8443 | --public-host HOST] --agent-id ID --role transit|landing [--port 8443] [--ttl 10m]
  hub-dispatch [--state-dir DIR] --text "/status all"
  hub-dispatch [--state-dir DIR] --text "/link transit-hk landing-hk [auth_user] [endpoint_name] [--mode direct|mesh]"
  hub-dispatch [--state-dir DIR] --text "/update <hub|all|transit|landing|agent_id> <version|latest> [--restart]"
  hub-tasks [--state-dir DIR] [--json]
  hub-results [--state-dir DIR] [--batch-id ID] [--json]
  hub-alert-offline [--state-dir DIR] [--dry-run]
  hub-alerts [--state-dir DIR] [--json]
  hub-alert-callback [--state-dir DIR] --data rp:obs:TOKEN
  hub-daemon [--state-dir DIR] [--host 127.0.0.1] [--port 8080] [--tls-cert CRT --tls-key KEY --client-ca CA --require-client-cert]
  bot-daemon [--state-dir DIR]
  agent-enroll --invite INVITE [--poll-once]
  agent-enroll --code CODE --hub-url https://HUB:8443 --agent-id ID --role transit|landing [--ca-cert CA]
  agent-enroll --bundle BUNDLE [--poll-once]
  agent-poll-once --hub-url URL --agent-id ID --role transit|landing --token-file FILE [--ca-cert CA --client-cert CRT --client-key KEY]
  agent-poll-loop --hub-url URL --agent-id ID --role transit|landing --token-file FILE [--ca-cert CA --client-cert CRT --client-key KEY]
  agent-poll-once --enrollment-file /etc/relaypilot/agent-enrollment.json
  agent-poll-loop --enrollment-file /etc/relaypilot/agent-enrollment.json
`)
}

func parseStateDirAndOneArg(rest []string, usage string) (string, string, error) {
	stateDir := defaultStateDir
	positionals := []string{}
	for i := 0; i < len(rest); i++ {
		item := rest[i]
		if item == "--state-dir" {
			if i+1 >= len(rest) {
				return "", "", errors.New("--state-dir requires a value")
			}
			stateDir = rest[i+1]
			i++
			continue
		}
		if strings.HasPrefix(item, "--state-dir=") {
			stateDir = strings.TrimPrefix(item, "--state-dir=")
			continue
		}
		positionals = append(positionals, item)
	}
	if len(positionals) != 1 {
		return "", "", errors.New(usage)
	}
	return stateDir, positionals[0], nil
}

func now() int64 { return time.Now().Unix() }

func ensureSafeName(value, field string) (string, error) {
	if !safeNameRE.MatchString(value) {
		return "", fmt.Errorf("%s must match %s: %q", field, safeNameRE.String(), value)
	}
	return value, nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func int64Value(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	default:
		return 0
	}
}

func asObj(v any) obj {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return obj{}
}

func asList(v any) []any {
	if xs, ok := v.([]any); ok {
		return xs
	}
	return nil
}

func loadJSON(path string) (obj, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > maxJSONFileBytes {
		return nil, fmt.Errorf("json file too large: %s (%d bytes > %d)", path, info.Size(), maxJSONFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, maxJSONFileBytes+1))
	dec.UseNumber()
	var out obj
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJSON(path string, data any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.tmp.%d", filepath.Base(path), os.Getpid()))
	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	if err := os.WriteFile(tmp, buf, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

func migrateStateDir(fromDir, toDir string, force, dryRun bool) (obj, error) {
	if fromDir == "" || toDir == "" {
		return nil, errors.New("--from and --to are required")
	}
	fromClean, err := filepath.Abs(fromDir)
	if err != nil {
		return nil, err
	}
	toClean, err := filepath.Abs(toDir)
	if err != nil {
		return nil, err
	}
	if fromClean == toClean {
		return nil, errors.New("--from and --to must be different directories")
	}
	if rel, err := filepath.Rel(fromClean, toClean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "." {
		return nil, errors.New("--to must not be inside --from")
	}
	if rel, err := filepath.Rel(toClean, fromClean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "." {
		return nil, errors.New("--from must not be inside --to")
	}
	info, err := os.Stat(fromClean)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--from is not a directory: %s", fromClean)
	}

	var copied []string
	var skipped []string
	var conflicts []string
	var createdDirs []string
	err = filepath.WalkDir(fromClean, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(fromClean, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(toClean, rel)
		if d.Type()&os.ModeSymlink != 0 {
			skipped = append(skipped, rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			createdDirs = append(createdDirs, rel)
			if dryRun {
				return nil
			}
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			skipped = append(skipped, rel)
			return nil
		}
		if _, err := os.Stat(target); err == nil && !force {
			conflicts = append(conflicts, rel)
			return nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		copied = append(copied, rel)
		if dryRun {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
	if err != nil {
		return nil, err
	}
	result := obj{
		"from":         fromClean,
		"to":           toClean,
		"dry_run":      dryRun,
		"force":        force,
		"created_dirs": createdDirs,
		"copied":       copied,
		"skipped":      skipped,
		"conflicts":    conflicts,
	}
	if len(conflicts) > 0 {
		result["ok"] = false
		result["message"] = "conflicting files exist; rerun with --force to overwrite"
		return result, nil
	}
	result["ok"] = true
	return result, nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func hubAgentsPath(stateDir string) string { return filepath.Join(stateDir, hubAgentsName) }
func hubAlertsPath(stateDir string) string { return filepath.Join(stateDir, hubAlertsName) }
func hubRemovedAgentsPath(stateDir string) string {
	return filepath.Join(stateDir, hubRemovedAgentsName)
}
func hubTasksDir(stateDir string) string        { return filepath.Join(stateDir, hubTasksDirName) }
func hubAgentTokensPath(stateDir string) string { return filepath.Join(stateDir, hubAgentTokensName) }
func hubAuthNoncesPath(stateDir string) string  { return filepath.Join(stateDir, hubAuthNoncesName) }
func hubEnrollCodesPath(stateDir string) string { return filepath.Join(stateDir, hubEnrollCodesName) }
func hubTLSDir(stateDir string) string          { return filepath.Join(stateDir, hubTLSDirName) }
func hubTLSCACertPath(stateDir string) string {
	return filepath.Join(hubTLSDir(stateDir), hubTLSCACertName)
}
func hubTLSCAKeyPath(stateDir string) string {
	return filepath.Join(hubTLSDir(stateDir), hubTLSCAKeyName)
}
func hubTLSServerCertPath(stateDir string) string {
	return filepath.Join(hubTLSDir(stateDir), hubTLSServerCertName)
}
func hubTLSServerKeyPath(stateDir string) string {
	return filepath.Join(hubTLSDir(stateDir), hubTLSServerKeyName)
}
func agentEnrollmentPath(stateDir string) string { return filepath.Join(stateDir, agentEnrollmentName) }

type hubTLSInitOptions struct {
	Hosts []string
	Days  int
	Force bool
}

type hubTLSBundle struct {
	CACertPath     string   `json:"ca_cert_path"`
	CAKeyPath      string   `json:"ca_key_path"`
	ServerCertPath string   `json:"server_cert_path"`
	ServerKeyPath  string   `json:"server_key_path"`
	Hosts          []string `json:"hosts"`
	NotAfter       int64    `json:"not_after"`
}

type agentTLSCertBundle struct {
	AgentID  string `json:"agent_id"`
	CertPath string `json:"cert_path"`
	KeyPath  string `json:"key_path"`
	CAPath   string `json:"ca_cert_path"`
	NotAfter int64  `json:"not_after"`
}

type hubListenOptions struct {
	TLSCertPath       string
	TLSKeyPath        string
	ClientCAPath      string
	RequireClientCert bool
}

type hubAgentProvisionOptions struct {
	HubURL  string
	AgentID string
	Role    string
	Name    string
	Labels  string
	Days    int
}

type hubEnrollCodeOptions struct {
	HubURL     string
	AgentID    string
	Role       string
	Name       string
	Labels     string
	TTLSeconds int64
	Days       int
}

type hubPublicURLOptions struct {
	ExplicitURL string
	PublicHost  string
	Port        int
}

type agentEnrollOptions struct {
	StateDir      string
	HubURL        string
	Code          string
	AgentID       string
	Role          string
	Name          string
	Labels        string
	CACertPath    string
	TLSServerName string
}

type agentEnrollmentConfig struct {
	HubURL         string `json:"hub_url"`
	AgentID        string `json:"agent_id"`
	Role           string `json:"role"`
	TokenFile      string `json:"token_file"`
	CACertPath     string `json:"ca_cert"`
	ClientCertPath string `json:"client_cert"`
	ClientKeyPath  string `json:"client_key"`
	CreatedAt      int64  `json:"created_at"`
}

func splitCSV(value string) []string {
	var out []string
	for _, raw := range strings.Split(value, ",") {
		item := strings.TrimSpace(raw)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func initHubTLS(stateDir string, opts hubTLSInitOptions) (hubTLSBundle, error) {
	if opts.Days <= 0 {
		opts.Days = defaultHubTLSDays
	}
	paths := []string{
		hubTLSCACertPath(stateDir),
		hubTLSCAKeyPath(stateDir),
		hubTLSServerCertPath(stateDir),
		hubTLSServerKeyPath(stateDir),
	}
	if !opts.Force {
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return hubTLSBundle{}, fmt.Errorf("TLS file already exists: %s; use --force to overwrite", path)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return hubTLSBundle{}, err
			}
		}
	}
	if err := os.MkdirAll(hubTLSDir(stateDir), 0o700); err != nil {
		return hubTLSBundle{}, err
	}
	hosts := normalizeTLSHosts(opts.Hosts)
	notAfter := time.Now().Add(time.Duration(opts.Days) * 24 * time.Hour)
	caCert, caKey, err := generateCA("RelayPilot Hub CA", notAfter)
	if err != nil {
		return hubTLSBundle{}, err
	}
	serverCertPEM, serverKeyPEM, err := generateSignedTLSCert(certRequest{
		CommonName:  "relaypilot-hub",
		Hosts:       hosts,
		NotAfter:    notAfter,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CA:          caCert,
		CAKey:       caKey,
	})
	if err != nil {
		return hubTLSBundle{}, err
	}
	if err := writePEMFile(hubTLSCACertPath(stateDir), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}), 0o644); err != nil {
		return hubTLSBundle{}, err
	}
	caKeyPEM, err := marshalECDSAPrivateKey(caKey)
	if err != nil {
		return hubTLSBundle{}, err
	}
	if err := writePEMFile(hubTLSCAKeyPath(stateDir), caKeyPEM, 0o600); err != nil {
		return hubTLSBundle{}, err
	}
	if err := writePEMFile(hubTLSServerCertPath(stateDir), serverCertPEM, 0o644); err != nil {
		return hubTLSBundle{}, err
	}
	if err := writePEMFile(hubTLSServerKeyPath(stateDir), serverKeyPEM, 0o600); err != nil {
		return hubTLSBundle{}, err
	}
	return hubTLSBundle{
		CACertPath:     hubTLSCACertPath(stateDir),
		CAKeyPath:      hubTLSCAKeyPath(stateDir),
		ServerCertPath: hubTLSServerCertPath(stateDir),
		ServerKeyPath:  hubTLSServerKeyPath(stateDir),
		Hosts:          hosts,
		NotAfter:       notAfter.Unix(),
	}, nil
}

func issueHubAgentClientCert(stateDir, agentID, outputDir string, days int) (agentTLSCertBundle, error) {
	if _, err := ensureSafeName(agentID, "agent.id"); err != nil {
		return agentTLSCertBundle{}, err
	}
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return agentTLSCertBundle{}, err
	}
	found := false
	for _, agent := range agents {
		if str(agent["id"]) == agentID {
			found = true
			break
		}
	}
	if !found {
		return agentTLSCertBundle{}, fmt.Errorf("agent must be imported before issuing certificate: %s", agentID)
	}
	if days <= 0 {
		days = defaultAgentCertDays
	}
	if outputDir == "" {
		outputDir = filepath.Join(hubTLSDir(stateDir), "agents", agentID)
	}
	caCert, caKey, err := loadX509CertAndECDSAKey(hubTLSCACertPath(stateDir), hubTLSCAKeyPath(stateDir))
	if err != nil {
		return agentTLSCertBundle{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return agentTLSCertBundle{}, err
	}
	notAfter := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	certPEM, keyPEM, err := generateHubAgentClientCertPEM(agentID, caCert, caKey, notAfter)
	if err != nil {
		return agentTLSCertBundle{}, err
	}
	certPath := filepath.Join(outputDir, agentID+".crt")
	keyPath := filepath.Join(outputDir, agentID+".key")
	if err := writePEMFile(certPath, certPEM, 0o644); err != nil {
		return agentTLSCertBundle{}, err
	}
	if err := writePEMFile(keyPath, keyPEM, 0o600); err != nil {
		return agentTLSCertBundle{}, err
	}
	return agentTLSCertBundle{AgentID: agentID, CertPath: certPath, KeyPath: keyPath, CAPath: hubTLSCACertPath(stateDir), NotAfter: notAfter.Unix()}, nil
}

func buildHubAgentRegistration(agentID, role, name, labelsValue string) (obj, error) {
	labels, err := parseLabels(labelsValue)
	if err != nil {
		return nil, err
	}
	return validateRegistration(obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        agentID,
		"role":      role,
		"name":      firstNonEmpty(name, agentID),
		"labels":    labels,
		"transport": "poll",
		"topology":  obj{"links": []any{}, "endpoints": []any{}, "inbounds": []any{}},
		"health":    obj{"status": "unknown"},
	})
}

func saveHubAgentRegistration(stateDir string, registration obj) error {
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return err
	}
	agentID := str(registration["id"])
	agents := asObj(reg["agents"])
	agents[agentID] = registration
	reg["agents"] = agents
	return saveHubRegistry(stateDir, reg)
}

func provisionHubAgentBundle(stateDir string, opts hubAgentProvisionOptions) (obj, error) {
	if opts.HubURL == "" {
		return nil, errors.New("--hub-url is required")
	}
	u, err := url.Parse(opts.HubURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("--hub-url must be an https URL, e.g. https://hub.example:8443")
	}
	if opts.AgentID == "" {
		return nil, errors.New("--agent-id is required")
	}
	registration, err := buildHubAgentRegistration(opts.AgentID, opts.Role, opts.Name, opts.Labels)
	if err != nil {
		return nil, err
	}
	if err := saveHubAgentRegistration(stateDir, registration); err != nil {
		return nil, err
	}
	tokenInfo, err := issueHubAgentToken(stateDir, opts.AgentID, "")
	if err != nil {
		return nil, err
	}
	caCert, caKey, err := loadX509CertAndECDSAKey(hubTLSCACertPath(stateDir), hubTLSCAKeyPath(stateDir))
	if err != nil {
		return nil, err
	}
	if opts.Days <= 0 {
		opts.Days = defaultAgentCertDays
	}
	notAfter := time.Now().Add(time.Duration(opts.Days) * 24 * time.Hour)
	certPEM, keyPEM, err := generateHubAgentClientCertPEM(opts.AgentID, caCert, caKey, notAfter)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(hubTLSCACertPath(stateDir))
	if err != nil {
		return nil, err
	}
	bundleObj := obj{
		"kind":            "relaypilot/agent-enrollment-bundle",
		"version":         version,
		"hub_url":         opts.HubURL,
		"agent_id":        opts.AgentID,
		"role":            opts.Role,
		"name":            firstNonEmpty(opts.Name, opts.AgentID),
		"labels":          asObj(registration["labels"]),
		"token":           str(tokenInfo["token"]),
		"ca_cert_pem":     string(caPEM),
		"client_cert_pem": string(certPEM),
		"client_key_pem":  string(keyPEM),
		"created_at":      now(),
		"not_after":       notAfter.Unix(),
	}
	bundle, err := encodeEnrollmentBundle(bundleObj)
	if err != nil {
		return nil, err
	}
	return obj{
		"agent_id":        opts.AgentID,
		"role":            opts.Role,
		"hub_url":         opts.HubURL,
		"bundle":          bundle,
		"not_after":       notAfter.Unix(),
		"secret_preview":  obj{"token": "stored inside bundle only", "client_key": "stored inside bundle only"},
		"install_command": fmt.Sprintf("relaypilot agent enroll --bundle '%s'", bundle),
	}, nil
}

func defaultHubEnrollCodes() obj {
	return obj{"kind": "relaypilot/hub-enroll-codes", "version": version, "codes": obj{}}
}

func loadHubEnrollCodes(stateDir string) (obj, error) {
	path := hubEnrollCodesPath(stateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return defaultHubEnrollCodes(), nil
	}
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	if _, ok := data["codes"].(map[string]any); !ok {
		data["codes"] = obj{}
	}
	if str(data["kind"]) == "" {
		data["kind"] = "relaypilot/hub-enroll-codes"
	}
	if int64Value(data["version"]) == 0 {
		data["version"] = version
	}
	return data, nil
}

func saveHubEnrollCodes(stateDir string, data obj) error {
	return writeJSON(hubEnrollCodesPath(stateDir), data, 0o600)
}

func createHubEnrollInvite(stateDir string, opts hubEnrollCodeOptions) (obj, error) {
	hubURL, err := normalizeHTTPSHubURL(opts.HubURL)
	if err != nil {
		return nil, err
	}
	registration, err := buildHubAgentRegistration(opts.AgentID, opts.Role, opts.Name, opts.Labels)
	if err != nil {
		return nil, err
	}
	if opts.TTLSeconds <= 0 {
		opts.TTLSeconds = defaultEnrollCodeTTLSeconds
	}
	if opts.Days <= 0 {
		opts.Days = defaultAgentCertDays
	}
	code, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	codeHash, err := hashToken(code)
	if err != nil {
		return nil, err
	}
	codeID := mustRandomHex(6)
	createdAt := now()
	expiresAt := createdAt + opts.TTLSeconds
	store, err := loadHubEnrollCodes(stateDir)
	if err != nil {
		return nil, err
	}
	codes := asObj(store["codes"])
	codes[codeID] = obj{
		"code_id":      codeID,
		"code_sha256":  codeHash,
		"hub_url":      hubURL,
		"agent_id":     registration["id"],
		"role":         registration["role"],
		"name":         registration["name"],
		"labels":       asObj(registration["labels"]),
		"created_at":   createdAt,
		"expires_at":   expiresAt,
		"max_uses":     1,
		"uses":         0,
		"cert_days":    opts.Days,
		"transport":    "poll",
		"used_at":      0,
		"created_mode": "invite",
	}
	store["codes"] = codes
	if err := saveHubEnrollCodes(stateDir, store); err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(hubTLSCACertPath(stateDir))
	if err != nil {
		return nil, err
	}
	inviteObj := obj{
		"kind":        "relaypilot/agent-enroll-invite",
		"version":     version,
		"hub_url":     hubURL,
		"code_id":     codeID,
		"code":        code,
		"agent_id":    registration["id"],
		"role":        registration["role"],
		"name":        registration["name"],
		"labels":      asObj(registration["labels"]),
		"ca_cert_pem": string(caPEM),
		"created_at":  createdAt,
		"expires_at":  expiresAt,
	}
	invite, err := encodeEnrollInvite(inviteObj)
	if err != nil {
		return nil, err
	}
	return obj{
		"agent_id":       registration["id"],
		"role":           registration["role"],
		"hub_url":        hubURL,
		"code_id":        codeID,
		"code":           code,
		"invite":         invite,
		"expires_at":     expiresAt,
		"ttl_seconds":    opts.TTLSeconds,
		"secret_preview": obj{"code": "stored inside invite; single-use"},
		"agent_command":  fmt.Sprintf("relaypilot agent enroll --invite '%s'", invite),
		"install_command": fmt.Sprintf(
			"bash <(curl -fsSL https://github.com/jiwen77/relaypilot/raw/main/install-relaypilot.sh) --enroll '%s'",
			invite,
		),
	}, nil
}

func normalizeHTTPSHubURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("--hub-url must be an https URL, e.g. https://hub.example:8443")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("--hub-url must not include a path")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("--hub-url must not include query or fragment")
	}
	return "https://" + u.Host, nil
}

func resolveHubPublicURL(opts hubPublicURLOptions, detect func() (string, error)) (string, error) {
	if strings.TrimSpace(opts.ExplicitURL) != "" {
		return normalizeHTTPSHubURL(opts.ExplicitURL)
	}
	port := opts.Port
	if port <= 0 {
		port = 8443
	}
	host := strings.TrimSpace(opts.PublicHost)
	if host == "" {
		if detect == nil {
			return "", errors.New("--hub-url or --public-host is required because public IP detection is unavailable")
		}
		detected, err := detect()
		if err != nil {
			return "", fmt.Errorf("could not detect public Hub IP; pass --hub-url or --public-host: %w", err)
		}
		host = strings.TrimSpace(detected)
	}
	if host == "" {
		return "", errors.New("public Hub host is empty; pass --hub-url or --public-host")
	}
	host, hostPort, err := normalizePublicHubHost(host)
	if err != nil {
		return "", err
	}
	if hostPort > 0 {
		port = hostPort
	}
	if port == 443 {
		return normalizeHTTPSHubURL("https://" + formatHostForURL(host))
	}
	return normalizeHTTPSHubURL(fmt.Sprintf("https://%s:%d", formatHostForURL(host), port))
}

func normalizePublicHubHost(raw string) (string, int, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", 0, errors.New("public Hub host is empty")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" {
			return "", 0, fmt.Errorf("public Hub host must be an https URL, DNS name, or IP, got %q", raw)
		}
		if u.Path != "" && u.Path != "/" {
			return "", 0, fmt.Errorf("public Hub host must not include a path: %q", raw)
		}
		port, err := parseOptionalPort(u.Port())
		if err != nil {
			return "", 0, err
		}
		return u.Hostname(), port, nil
	}
	if strings.Contains(raw, "/") {
		return "", 0, fmt.Errorf("public Hub host must be a host name or IP, got %q", raw)
	}
	if host, portString, err := net.SplitHostPort(raw); err == nil {
		port, err := parseOptionalPort(portString)
		if err != nil {
			return "", 0, err
		}
		return strings.Trim(host, "[]"), port, nil
	}
	return strings.Trim(raw, "[]"), 0, nil
}

func parseOptionalPort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid Hub port: %q", raw)
	}
	return port, nil
}

func formatHostForURL(host string) string {
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func detectPublicIP() (string, error) {
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
	}
	client := &http.Client{Timeout: 5 * time.Second}
	var errs []string
	for _, endpoint := range endpoints {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		req.Header.Set("User-Agent", "RelayPilot/"+buildVersion)
		resp, err := client.Do(req)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		data, readErr := readLimited(resp.Body, 256)
		_ = resp.Body.Close()
		if readErr != nil {
			errs = append(errs, readErr.Error())
			continue
		}
		if resp.StatusCode >= 400 {
			errs = append(errs, fmt.Sprintf("%s returned %d", endpoint, resp.StatusCode))
			continue
		}
		host := strings.TrimSpace(string(data))
		if ip := net.ParseIP(host); ip != nil {
			return host, nil
		}
		errs = append(errs, fmt.Sprintf("%s returned non-IP %q", endpoint, host))
	}
	return "", errors.New(strings.Join(errs, "; "))
}

func generateHubAgentClientCertPEM(agentID string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, notAfter time.Time) ([]byte, []byte, error) {
	return generateSignedTLSCert(certRequest{
		CommonName:  agentID,
		NotAfter:    notAfter,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CA:          caCert,
		CAKey:       caKey,
	})
}

func encodeEnrollmentBundle(bundle obj) (string, error) {
	wire, err := json.Marshal(bundle)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(wire), nil
}

func decodeEnrollmentBundle(bundle string) (obj, error) {
	bundle = strings.TrimSpace(bundle)
	if bundle == "" {
		return nil, errors.New("--bundle is required")
	}
	decoders := []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding}
	var data []byte
	var err error
	for _, enc := range decoders {
		data, err = enc.DecodeString(bundle)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxHTTPBodyBytes {
		return nil, fmt.Errorf("enrollment bundle too large: %d bytes > %d", len(data), maxHTTPBodyBytes)
	}
	var out obj
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if str(out["kind"]) != "relaypilot/agent-enrollment-bundle" || int64Value(out["version"]) != version {
		return nil, errors.New("unsupported enrollment bundle")
	}
	return out, nil
}

func encodeEnrollInvite(invite obj) (string, error) {
	wire, err := json.Marshal(invite)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(wire), nil
}

func decodeEnrollInvite(invite string) (obj, error) {
	invite = strings.TrimSpace(invite)
	if invite == "" {
		return nil, errors.New("--invite is required")
	}
	decoders := []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding}
	var data []byte
	var err error
	for _, enc := range decoders {
		data, err = enc.DecodeString(invite)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxHTTPBodyBytes {
		return nil, fmt.Errorf("enrollment invite too large: %d bytes > %d", len(data), maxHTTPBodyBytes)
	}
	var out obj
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if str(out["kind"]) != "relaypilot/agent-enroll-invite" || int64Value(out["version"]) != version {
		return nil, errors.New("unsupported enrollment invite")
	}
	return out, nil
}

func agentEnrollBundle(bundle string, opts agentEnrollOptions) (obj, error) {
	if opts.StateDir == "" {
		opts.StateDir = defaultStateDir
	}
	decoded, err := decodeEnrollmentBundle(bundle)
	if err != nil {
		return nil, err
	}
	if _, err := ensureSafeName(str(decoded["agent_id"]), "agent.id"); err != nil {
		return nil, err
	}
	role := str(decoded["role"])
	if role != "transit" && role != "landing" && role != "hub" {
		return nil, fmt.Errorf("agent.role must be transit, landing, or hub")
	}
	hubURL := str(decoded["hub_url"])
	if u, err := url.Parse(hubURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("enrollment hub_url must be https")
	}
	token := strings.TrimSpace(str(decoded["token"]))
	if token == "" {
		return nil, errors.New("enrollment token is empty")
	}
	return writeAgentEnrollmentFromPayload(decoded, token, opts.StateDir)
}

func agentEnrollInvite(invite string, opts agentEnrollOptions, timeout time.Duration) (obj, error) {
	if opts.StateDir == "" {
		opts.StateDir = defaultStateDir
	}
	decoded, err := decodeEnrollInvite(invite)
	if err != nil {
		return nil, err
	}
	if opts.HubURL == "" {
		opts.HubURL = str(decoded["hub_url"])
	}
	if opts.Code == "" {
		opts.Code = str(decoded["code"])
	}
	if opts.AgentID == "" {
		opts.AgentID = str(decoded["agent_id"])
	}
	if opts.Role == "" {
		opts.Role = str(decoded["role"])
	}
	if opts.Name == "" {
		opts.Name = str(decoded["name"])
	}
	labelsObj := asObj(decoded["labels"])
	hubURL, err := normalizeHTTPSHubURL(opts.HubURL)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := tlsConfigFromCAPEM(str(decoded["ca_cert_pem"]), opts.TLSServerName)
	if err != nil {
		return nil, err
	}
	payload := obj{
		"hub_url":  hubURL,
		"code":     opts.Code,
		"code_id":  str(decoded["code_id"]),
		"agent_id": opts.AgentID,
		"role":     opts.Role,
		"name":     opts.Name,
		"labels":   labelsObj,
	}
	resp, err := httpJSONRequestWithTLS("POST", hubURL+"/api/enroll", payload, "", "", timeout, tlsConfig)
	if err != nil {
		return nil, err
	}
	enrollment := asObj(resp["enrollment"])
	token := strings.TrimSpace(str(enrollment["token"]))
	if token == "" {
		return nil, errors.New("Hub enrollment response did not include a token")
	}
	return writeAgentEnrollmentFromPayload(enrollment, token, opts.StateDir)
}

func agentEnrollCode(opts agentEnrollOptions, timeout time.Duration) (obj, error) {
	if opts.StateDir == "" {
		opts.StateDir = defaultStateDir
	}
	if strings.TrimSpace(opts.Code) == "" {
		return nil, errors.New("--code is required")
	}
	hubURL, err := normalizeHTTPSHubURL(opts.HubURL)
	if err != nil {
		return nil, err
	}
	labels, err := parseLabels(opts.Labels)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := loadAgentTLSConfig(opts.CACertPath, "", "", opts.TLSServerName)
	if err != nil {
		return nil, err
	}
	payload := obj{
		"hub_url":  hubURL,
		"code":     opts.Code,
		"agent_id": opts.AgentID,
		"role":     opts.Role,
		"name":     opts.Name,
		"labels":   labels,
	}
	resp, err := httpJSONRequestWithTLS("POST", hubURL+"/api/enroll", payload, "", "", timeout, tlsConfig)
	if err != nil {
		return nil, err
	}
	enrollment := asObj(resp["enrollment"])
	token := strings.TrimSpace(str(enrollment["token"]))
	if token == "" {
		return nil, errors.New("Hub enrollment response did not include a token")
	}
	return writeAgentEnrollmentFromPayload(enrollment, token, opts.StateDir)
}

func tlsConfigFromCAPEM(caPEM, serverName string) (*tls.Config, error) {
	caPEM = strings.TrimSpace(caPEM)
	if caPEM == "" && serverName == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("invite did not contain a valid Hub CA certificate")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func writeAgentEnrollmentFromPayload(decoded obj, token, stateDir string) (obj, error) {
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	agentID, err := ensureSafeName(str(decoded["agent_id"]), "agent.id")
	if err != nil {
		return nil, err
	}
	role := str(decoded["role"])
	if role != "transit" && role != "landing" && role != "hub" {
		return nil, fmt.Errorf("agent.role must be transit, landing, or hub")
	}
	hubURL := str(decoded["hub_url"])
	if _, err := normalizeHTTPSHubURL(hubURL); err != nil {
		return nil, errors.New("enrollment hub_url must be https")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("enrollment token is empty")
	}
	paths := agentEnrollmentConfig{
		HubURL:         hubURL,
		AgentID:        agentID,
		Role:           role,
		TokenFile:      filepath.Join(stateDir, "agent-token"),
		CACertPath:     filepath.Join(stateDir, "hub-ca.crt"),
		ClientCertPath: filepath.Join(stateDir, "agent.crt"),
		ClientKeyPath:  filepath.Join(stateDir, "agent.key"),
		CreatedAt:      now(),
	}
	if err := writePEMFile(paths.CACertPath, []byte(str(decoded["ca_cert_pem"])), 0o644); err != nil {
		return nil, err
	}
	if err := writePEMFile(paths.ClientCertPath, []byte(str(decoded["client_cert_pem"])), 0o644); err != nil {
		return nil, err
	}
	if err := writePEMFile(paths.ClientKeyPath, []byte(str(decoded["client_key_pem"])), 0o600); err != nil {
		return nil, err
	}
	if err := writePEMFile(paths.TokenFile, []byte(token+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := writeJSON(agentEnrollmentPath(stateDir), paths, 0o600); err != nil {
		return nil, err
	}
	return obj{
		"agent_id":        paths.AgentID,
		"role":            paths.Role,
		"hub_url":         paths.HubURL,
		"enrollment_file": agentEnrollmentPath(stateDir),
		"token_file":      paths.TokenFile,
		"ca_cert":         paths.CACertPath,
		"client_cert":     paths.ClientCertPath,
		"client_key":      paths.ClientKeyPath,
		"poll_once":       fmt.Sprintf("relaypilot agent poll-once --enrollment-file %s", agentEnrollmentPath(stateDir)),
		"install_service": fmt.Sprintf("relaypilot agent install-service --enrollment-file %s", agentEnrollmentPath(stateDir)),
	}, nil
}

func redeemHubEnrollCode(stateDir string, payload obj) (obj, error) {
	code := strings.TrimSpace(str(payload["code"]))
	if code == "" {
		return nil, errors.New("enrollment code is required")
	}
	hubURL, err := normalizeHTTPSHubURL(str(payload["hub_url"]))
	if err != nil {
		return nil, err
	}
	codeHash, err := hashToken(code)
	if err != nil {
		return nil, err
	}
	store, err := loadHubEnrollCodes(stateDir)
	if err != nil {
		return nil, err
	}
	codes := asObj(store["codes"])
	codeID := str(payload["code_id"])
	var matchedKey string
	var matched obj
	if codeID != "" {
		candidate := asObj(codes[codeID])
		if str(candidate["code_sha256"]) == codeHash {
			matchedKey = codeID
			matched = candidate
		}
	}
	if len(matched) == 0 {
		for key, raw := range codes {
			candidate := asObj(raw)
			if str(candidate["code_sha256"]) == codeHash {
				matchedKey = key
				matched = candidate
				break
			}
		}
	}
	if len(matched) == 0 {
		return nil, errors.New("invalid enrollment code")
	}
	nowTs := now()
	if int64Value(matched["used_at"]) > 0 || int64Value(matched["uses"]) >= firstPositiveInt64(int64Value(matched["max_uses"]), 1) {
		return nil, errors.New("enrollment code has already been used")
	}
	if expiresAt := int64Value(matched["expires_at"]); expiresAt > 0 && nowTs > expiresAt {
		return nil, errors.New("enrollment code has expired")
	}
	boundAgentID := str(matched["agent_id"])
	boundHubURL := str(matched["hub_url"])
	if boundHubURL != "" && boundHubURL != hubURL {
		return nil, errors.New("enrollment code is bound to a different Hub URL")
	}
	requestAgentID := str(payload["agent_id"])
	if boundAgentID != "" && requestAgentID != "" && boundAgentID != requestAgentID {
		return nil, errors.New("enrollment code is bound to a different agent id")
	}
	agentID := firstNonEmpty(boundAgentID, requestAgentID)
	boundRole := str(matched["role"])
	requestRole := str(payload["role"])
	if boundRole != "" && requestRole != "" && boundRole != requestRole {
		return nil, errors.New("enrollment code is bound to a different role")
	}
	role := firstNonEmpty(boundRole, requestRole, "transit")
	name := firstNonEmpty(str(matched["name"]), str(payload["name"]), agentID)
	labels := asObj(matched["labels"])
	if len(labels) == 0 {
		labels = asObj(payload["labels"])
	}
	registration, err := validateRegistration(obj{
		"kind":      agentRegistrationKind,
		"version":   version,
		"id":        agentID,
		"role":      role,
		"name":      name,
		"labels":    labels,
		"transport": "poll",
		"topology":  obj{"links": []any{}, "endpoints": []any{}, "inbounds": []any{}},
		"health":    obj{"status": "unknown"},
	})
	if err != nil {
		return nil, err
	}
	if err := saveHubAgentRegistration(stateDir, registration); err != nil {
		return nil, err
	}
	tokenInfo, err := issueHubAgentToken(stateDir, agentID, "")
	if err != nil {
		return nil, err
	}
	caCert, caKey, err := loadX509CertAndECDSAKey(hubTLSCACertPath(stateDir), hubTLSCAKeyPath(stateDir))
	if err != nil {
		return nil, err
	}
	days := int(int64Value(matched["cert_days"]))
	if days <= 0 {
		days = defaultAgentCertDays
	}
	notAfter := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	certPEM, keyPEM, err := generateHubAgentClientCertPEM(agentID, caCert, caKey, notAfter)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(hubTLSCACertPath(stateDir))
	if err != nil {
		return nil, err
	}
	matched["used_at"] = nowTs
	matched["used_by"] = agentID
	matched["uses"] = int64Value(matched["uses"]) + 1
	codes[matchedKey] = matched
	store["codes"] = codes
	if err := saveHubEnrollCodes(stateDir, store); err != nil {
		return nil, err
	}
	return obj{
		"kind":            "relaypilot/agent-enrollment-bundle",
		"version":         version,
		"hub_url":         firstNonEmpty(boundHubURL, hubURL),
		"agent_id":        agentID,
		"role":            role,
		"name":            name,
		"labels":          labels,
		"token":           str(tokenInfo["token"]),
		"ca_cert_pem":     string(caPEM),
		"client_cert_pem": string(certPEM),
		"client_key_pem":  string(keyPEM),
		"created_at":      nowTs,
		"not_after":       notAfter.Unix(),
	}, nil
}

func firstPositiveInt64(v, fallback int64) int64 {
	if v > 0 {
		return v
	}
	return fallback
}

func loadAgentEnrollment(stateDir string) (agentEnrollmentConfig, error) {
	data, err := loadJSON(agentEnrollmentPath(stateDir))
	if err != nil {
		return agentEnrollmentConfig{}, err
	}
	return agentEnrollmentConfig{
		HubURL:         str(data["hub_url"]),
		AgentID:        str(data["agent_id"]),
		Role:           str(data["role"]),
		TokenFile:      str(data["token_file"]),
		CACertPath:     str(data["ca_cert"]),
		ClientCertPath: str(data["client_cert"]),
		ClientKeyPath:  str(data["client_key"]),
		CreatedAt:      int64Value(data["created_at"]),
	}, nil
}

func loadAgentEnrollmentFile(path string) (agentEnrollmentConfig, error) {
	data, err := loadJSON(path)
	if err != nil {
		return agentEnrollmentConfig{}, err
	}
	return agentEnrollmentConfig{
		HubURL:         str(data["hub_url"]),
		AgentID:        str(data["agent_id"]),
		Role:           str(data["role"]),
		TokenFile:      str(data["token_file"]),
		CACertPath:     str(data["ca_cert"]),
		ClientCertPath: str(data["client_cert"]),
		ClientKeyPath:  str(data["client_key"]),
		CreatedAt:      int64Value(data["created_at"]),
	}, nil
}

func applyAgentEnrollmentDefaults(path string, hubURL, agentID, role, tokenFile, caCert, clientCert, clientKey *string) error {
	if path == "" {
		return nil
	}
	cfg, err := loadAgentEnrollmentFile(path)
	if err != nil {
		return err
	}
	if *hubURL == "" {
		*hubURL = cfg.HubURL
	}
	if *agentID == "" {
		*agentID = cfg.AgentID
	}
	if *role == "" {
		*role = cfg.Role
	}
	if *tokenFile == "" {
		*tokenFile = cfg.TokenFile
	}
	if *caCert == "" {
		*caCert = cfg.CACertPath
	}
	if *clientCert == "" {
		*clientCert = cfg.ClientCertPath
	}
	if *clientKey == "" {
		*clientKey = cfg.ClientKeyPath
	}
	return nil
}

type certRequest struct {
	CommonName  string
	Hosts       []string
	NotAfter    time.Time
	ExtKeyUsage []x509.ExtKeyUsage
	CA          *x509.Certificate
	CAKey       *ecdsa.PrivateKey
}

func normalizeTLSHosts(hosts []string) []string {
	seen := map[string]bool{}
	add := func(v string, out *[]string) {
		v, _, _ = normalizePublicHubHost(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		*out = append(*out, v)
	}
	var out []string
	add("localhost", &out)
	add("127.0.0.1", &out)
	add("::1", &out)
	for _, host := range hosts {
		add(host, &out)
	}
	return out
}

func generateCA(commonName string, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func generateSignedTLSCert(req certRequest) ([]byte, []byte, error) {
	if req.CA == nil || req.CAKey == nil {
		return nil, nil, errors.New("CA certificate and key are required")
	}
	if req.NotAfter.IsZero() {
		req.NotAfter = time.Now().Add(time.Duration(defaultAgentCertDays) * 24 * time.Hour)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: req.CommonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              req.NotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           req.ExtKeyUsage,
		BasicConstraintsValid: true,
	}
	for _, host := range req.Hosts {
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, req.CA, &key.PublicKey, req.CAKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := marshalECDSAPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func marshalECDSAPrivateKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func writePEMFile(path string, data []byte, perm os.FileMode) error {
	if len(data) == 0 {
		return fmt.Errorf("empty PEM data for %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.tmp.%d", filepath.Base(path), os.Getpid()))
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

func loadX509CertAndECDSAKey(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("invalid certificate PEM: %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("invalid private key PEM: %s", keyPath)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no certificates found in %s", path)
	}
	return pool, nil
}

func loadHubServerTLSConfig(certPath, keyPath, clientCAPath string, requireClientCert bool) (*tls.Config, error) {
	if certPath == "" || keyPath == "" {
		return nil, errors.New("--tls-cert and --tls-key are required for HTTPS Hub")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if clientCAPath != "" || requireClientCert {
		if clientCAPath == "" {
			return nil, errors.New("--client-ca is required when --require-client-cert is set")
		}
		pool, err := loadCertPool(clientCAPath)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		// The Hub currently serves enrollment and authenticated Agent APIs on the
		// same listener. Enrollment is the bootstrap step that issues the Agent's
		// client certificate, so the TLS handshake must allow clients without a
		// certificate. Authenticated Agent APIs still require and verify the client
		// certificate at the HTTP layer when --require-client-cert is set.
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return cfg, nil
}

func loadAgentTLSConfig(caCertPath, clientCertPath, clientKeyPath, serverName string) (*tls.Config, error) {
	if caCertPath == "" && clientCertPath == "" && clientKeyPath == "" && serverName == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caCertPath != "" {
		pool, err := loadCertPool(caCertPath)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if clientCertPath != "" || clientKeyPath != "" {
		if clientCertPath == "" || clientKeyPath == "" {
			return nil, errors.New("--client-cert and --client-key must be provided together")
		}
		cert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

func defaultHubRegistry() obj {
	return obj{"kind": "relaypilot/hub-registry", "version": version, "agents": obj{}}
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
	return writeJSON(hubAgentsPath(stateDir), reg, 0o600)
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
	return out, nil
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

func updateHeartbeat(stateDir, agentID string, topology, health obj) (obj, error) {
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
	agents[agentID] = agent
	reg["agents"] = agents
	return agent, saveHubRegistry(stateDir, reg)
}

func createHubTask(stateDir string, agent obj, command string, args []string, batchID, originText string) (obj, error) {
	taskID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	task := obj{"kind": hubTaskKind, "version": version, "id": taskID, "agent_id": agent["id"], "agent_role": agent["role"], "command": command, "args": stringsToAny(args), "status": "queued", "created_at": now(), "batch_id": batchID, "origin_text": originText}
	return task, writeJSON(filepath.Join(hubTasksDir(stateDir), taskID+".json"), task, 0o600)
}

func createHubTaskWithPayload(stateDir string, agent obj, command string, args []string, payload obj, batchID, originText string) (obj, error) {
	taskID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	task := obj{"kind": hubTaskKind, "version": version, "id": taskID, "agent_id": agent["id"], "agent_role": agent["role"], "command": command, "args": stringsToAny(args), "payload": payload, "status": "queued", "created_at": now(), "batch_id": batchID, "origin_text": originText}
	return task, writeJSON(filepath.Join(hubTasksDir(stateDir), taskID+".json"), task, 0o600)
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

func listHubTasks(stateDir string) ([]obj, error) {
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
		delete(task, "path")
		leased = append(leased, task)
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
	storedResult := result
	if str(task["command"]) == "export_endpoint" {
		storedResult = sanitizeTaskResult(result)
	}
	task["result"] = storedResult
	if str(task["command"]) == "bind_endpoint" {
		task["payload"] = sanitizeTaskPayload(asObj(task["payload"]))
	}
	if err := writeJSON(path, task, 0o600); err != nil {
		return nil, err
	}
	if str(task["command"]) == "export_endpoint" {
		if task["status"] == "done" {
			if err := queueBindEndpointFromExportResult(stateDir, task, result); err != nil {
				task["followup_error"] = err.Error()
				if writeErr := writeJSON(path, task, 0o600); writeErr != nil {
					return nil, writeErr
				}
			}
		}
		task["payload"] = sanitizeTaskPayload(asObj(task["payload"]))
		if writeErr := writeJSON(path, task, 0o600); writeErr != nil {
			return nil, writeErr
		}
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
			meshTransit["peer_endpoint"] = net.JoinHostPort(str(endpoint["server"]), strconv.Itoa(int(int64Value(firstNonNil(meshTransit["peer_endpoint_port"], meshSummary["port"])))))
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
		tasks, err := listHubTasks(stateDir)
		if err != nil {
			return "", err
		}
		batch := ""
		if len(args) > 0 {
			batch = args[0]
		}
		return formatHubTaskResultsText(tasks, batch), nil
	case "agents":
		return formatAgentsText(agents), nil
	case "topology", "tree":
		return formatHubTopologyText(agents), nil
	case "link", "connect":
		return hubLinkTransitLanding(stateDir, agents, args, text)
	case "update", "upgrade":
		return hubUpdateCommand(stateDir, agents, args, text)
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
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	var b strings.Builder
	fmt.Fprintf(&b, "📨 已下发 /%s 给 %d 个节点\n", command, len(matched))
	fmt.Fprintf(&b, "目标：%s\n", selector)
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

func parseUpdateArgs(args []string) (selector, updateVersion string, restart bool, err error) {
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
	if selector == "" || updateVersion == "" {
		return "", "", false, errors.New("usage: /update <hub|all|transit|landing|agent_id> <version|latest> [--restart]")
	}
	return selector, updateVersion, restart, nil
}

func hubUpdateCommand(stateDir string, agents []obj, args []string, originText string) (string, error) {
	selector, updateVersion, restart, err := parseUpdateArgs(args)
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
		return strings.Join(lines, "\n"), nil
	}
	matched := selectAgents(agents, selector)
	if len(matched) == 0 {
		return "", fmt.Errorf("no agents matched selector: %s", selector)
	}
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	payload := obj{"version": updateVersion, "restart_services": restart}
	var b strings.Builder
	fmt.Fprintf(&b, "📦 已下发 RelayPilot 更新给 %d 个节点\n", len(matched))
	fmt.Fprintf(&b, "目标：%s\n", selector)
	fmt.Fprintf(&b, "版本：%s\n", updateVersion)
	fmt.Fprintf(&b, "重启服务：%s\n", yesNo(restart))
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
		return "", fmt.Errorf("landing %s has no endpoint snapshot yet; wait for agent heartbeat or configure landing first", landingID)
	}
	if _, err := normalizeLinkMode(linkMode); err != nil {
		return "", err
	}
	batchID := fmt.Sprintf("%d-%s", now(), mustRandomHex(4))
	payload := obj{
		"transit_id":    transitID,
		"landing_id":    landingID,
		"endpoint_name": endpointName,
		"auth_user":     firstNonEmpty(authUser, endpointName),
		"link_mode":     linkMode,
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

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

func runCommand(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func findHubAgentByID(agents []obj, agentID string) (obj, error) {
	for _, agent := range agents {
		if str(agent["id"]) == agentID {
			return agent, nil
		}
	}
	return nil, fmt.Errorf("agent not found: %s", agentID)
}

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

func roleIcon(role string) string {
	if role == "transit" {
		return "🚦"
	}
	if role == "landing" {
		return "🎯"
	}
	return "🧭"
}

func formatAgentsText(agents []obj) string {
	if len(agents) == 0 {
		return "暂无已注册 agent。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📡 已注册 agent：%d\n", len(agents))
	for _, agent := range agents {
		labels := formatLabels(asObj(agent["labels"]))
		suffix := ""
		if labels != "" {
			suffix = " · " + labels
		}
		fmt.Fprintf(&b, "%s %s %s · %s%s\n", livenessIcon(agent), roleIcon(str(agent["role"])), str(agent["id"]), str(agent["name"]), suffix)
	}
	return strings.TrimRight(b.String(), "\n")
}

func livenessIcon(agent obj) string {
	age := now() - int64Value(firstNonNil(agent["last_seen"], agent["updated_at"]))
	if age <= heartbeatStaleSeconds {
		return "🟢"
	}
	if age <= heartbeatOfflineSeconds {
		return "🟡"
	}
	return "🔴"
}

func agentLiveness(agent obj) string {
	age := now() - int64Value(firstNonNil(agent["last_seen"], agent["updated_at"]))
	if age <= heartbeatStaleSeconds {
		return "online"
	}
	if age <= heartbeatOfflineSeconds {
		return "stale"
	}
	return "offline"
}

func hubStatusText(stateDir string, agents []obj) string {
	tasks, _ := listHubTasks(stateDir)
	transits, landings, queued, offline, stale := 0, 0, 0, 0, 0
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
		}
	}
	for _, t := range tasks {
		if str(t["status"]) == "queued" {
			queued++
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
	}
	return strings.Join([]string{
		fmt.Sprintf("%s Hub 管理面正常", statusIcon),
		fmt.Sprintf("节点：%d 个中转 / %d 个落地", transits, landings),
		fmt.Sprintf("异常：%d 离线 / %d 可能掉线", offline, stale),
		fmt.Sprintf("待处理任务：%d", queued),
		"默认不广播：/status 只看 Hub；需要巡检时用 /status all。",
		"查看链路：/topology",
	}, "\n")
}

func hubTelegramCommands() string {
	return formatTelegramCommands(false, true)
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

func formatTelegramCommands(botFather, hub bool) string {
	rows := selectedTelegramCommands(hub)
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
		lines = append(lines, branchPrefix+" "+formatTopologyAgentLine(transit, "🚦"))
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
			lines = append(lines, childPrefix+childBranch+" "+formatTopologyAgentLine(landing, "🎯")+linkText)
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
			lines = append(lines, prefix+" "+formatTopologyAgentLine(landing, "🎯"))
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
	var out []obj
	for _, task := range tasks {
		status := str(task["status"])
		if status != "done" && status != "failed" && status != "cancelled" {
			continue
		}
		if batchID != "" && str(task["batch_id"]) != batchID {
			continue
		}
		out = append(out, task)
	}
	return out
}

func formatHubTaskResultsText(tasks []obj, batchID string) string {
	selected := filterDoneTasks(tasks, batchID)
	if len(selected) == 0 {
		return "暂无已完成任务结果。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📬 任务结果：%d\n", len(selected))
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
		} else if str(res["error"]) != "" {
			fmt.Fprintf(&b, "error: %s\n", str(res["error"]))
		}
	}
	return strings.TrimRight(b.String(), "\n")
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
	batches[batchID] = obj{
		"batch_id":        batchID,
		"origin_text":     originText,
		"chat_id":         chatID,
		"created_at":      now(),
		"timeout_seconds": tgBatchResultTimeoutSec,
	}
	data["batches"] = batches
	return savePendingTGBatches(stateDir, data)
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
				ready = append(ready, obj{
					"batch_id":    batchID,
					"chat_id":     batch["chat_id"],
					"origin_text": batch["origin_text"],
					"text":        fmt.Sprintf("⏱️ 任务结果超时：%s\n没有找到该批次任务。", batchID),
				})
				delete(batches, batchID)
				changed = true
			}
			continue
		}
		if batchReady(matched) {
			ready = append(ready, obj{
				"batch_id":    batchID,
				"chat_id":     batch["chat_id"],
				"origin_text": batch["origin_text"],
				"text":        formatHubTaskResultsText(tasks, batchID),
			})
			delete(batches, batchID)
			changed = true
			continue
		}
		if pendingBatchExpired(batch) {
			ready = append(ready, obj{
				"batch_id":    batchID,
				"chat_id":     batch["chat_id"],
				"origin_text": batch["origin_text"],
				"text":        "⏱️ 部分任务仍未完成，当前结果：\n" + formatHubTaskResultsText(tasks, batchID),
			})
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

func telegramConfigPath(stateDir string) string { return filepath.Join(stateDir, telegramConfigName) }

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

func htmlCode(text string) string {
	return "<code>" + html.EscapeString(text) + "</code>"
}

func hubPanelReply(stateDir string) telegramReply {
	agents, err := listHubAgents(stateDir)
	if err != nil {
		return telegramReply{Text: "❌ " + err.Error()}
	}
	tasks, _ := listHubTasks(stateDir)
	host, _ := os.Hostname()
	if host == "" {
		host = "hub"
	}
	transits, landings, online, stale, offline, queued := 0, 0, 0, 0, 0, 0
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
		}
	}
	for _, task := range tasks {
		if str(task["status"]) == "queued" {
			queued++
		}
	}
	lines := []string{
		"🛡 <b>RelayPilot 控制中枢</b>",
		"Hub：" + htmlCode(host),
		"当前版本：" + htmlCode(buildVersion),
		fmt.Sprintf("节点：%d 在线 / %d 可能掉线 / %d 离线", online, stale, offline),
		fmt.Sprintf("Transit：%d · Landing：%d", transits, landings),
		fmt.Sprintf("待处理任务：%d", queued),
		"",
		"更新中心只给出可复制命令，不会误触执行。",
	}
	return telegramReply{
		Text:      strings.Join(lines, "\n"),
		ParseMode: "HTML",
		ReplyMarkup: tgKeyboard(
			[]any{tgButton("🔄 刷新状态", "rp:panel")},
			[]any{tgButton("🌐 拓扑", "rp:topology"), tgButton("📊 节点", "rp:agents")},
			[]any{tgButton("📬 任务结果", "rp:results"), tgButton("⬆️ 更新中心", "rp:update")},
			[]any{tgURLButton("⭐ GitHub", "https://github.com/jiwen77/relaypilot")},
		),
		CallbackText: "已刷新",
	}
}

func defaultUpdateExampleVersion() string {
	if strings.HasPrefix(buildVersion, "v") {
		return buildVersion
	}
	return "v0.1.0"
}

func updateCenterReply() telegramReply {
	exampleVersion := defaultUpdateExampleVersion()
	lines := []string{
		"⬆️ <b>RelayPilot 更新中心</b>",
		"当前 Hub 版本：" + htmlCode(buildVersion),
		"",
		"建议顺序：Hub → 单个 canary 节点 → 按角色批量 → 全 Agent。",
		"把示例里的版本替换为 GitHub Release tag。",
		"",
		"<b>可复制命令：</b>",
		htmlCode("/relaypilot_update hub " + exampleVersion),
		htmlCode("/relaypilot_update transit-hk " + exampleVersion + " --restart"),
		htmlCode("/relaypilot_update transit " + exampleVersion + " --restart"),
		htmlCode("/relaypilot_update landing " + exampleVersion + " --restart"),
		htmlCode("/relaypilot_update all " + exampleVersion + " --restart"),
		"",
		"不建议直接从未知版本执行 all；先单节点验证。",
	}
	return telegramReply{
		Text:         strings.Join(lines, "\n"),
		ParseMode:    "HTML",
		ReplyMarkup:  tgBackKeyboard(),
		CallbackText: "更新中心",
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

func handleTelegramHubUpdate(stateDir string, update obj) string {
	return handleTelegramHubReply(stateDir, update).Text
}

func handleTelegramHubReply(stateDir string, update obj) telegramReply {
	if cb := asObj(update["callback_query"]); len(cb) > 0 {
		data := str(cb["data"])
		if data == "" {
			return telegramReply{}
		}
		switch data {
		case "rp:panel":
			return hubPanelReply(stateDir)
		case "rp:update":
			return updateCenterReply()
		case "rp:topology":
			out, err := hubDispatchCommand(stateDir, "/topology")
			if err != nil {
				return textPanelReply("❌ " + err.Error())
			}
			return textPanelReply(out)
		case "rp:agents":
			out, err := hubDispatchCommand(stateDir, "/agents")
			if err != nil {
				return textPanelReply("❌ " + err.Error())
			}
			return textPanelReply(out)
		case "rp:results":
			out, err := hubDispatchCommand(stateDir, "/results")
			if err != nil {
				return textPanelReply("❌ " + err.Error())
			}
			return textPanelReply(out)
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
	command := normalizeTelegramCommand(strings.Fields(text)[0])
	if command == "start" || command == "panel" {
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
	case "bind_endpoint":
		return executeBindEndpointTask(task, stateDir, conf)
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
	if len(meshSummary) > 0 {
		result["mesh"] = meshSummary
		result["text"] = fmt.Sprintf("mesh ready and endpoint exported: %s", endpoint["name"])
	}
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
	result := obj{
		"success":       true,
		"command":       "bind_endpoint",
		"endpoint_name": endpoint["name"],
		"endpoint_tag":  endpoint["tag"],
		"link_mode":     firstNonEmpty(str(payload["link_mode"]), str(endpoint["link_mode"]), "direct"),
		"auth_user":     authUser,
		"summary":       summary,
		"text":          fmt.Sprintf("linked auth_user %s to endpoint %s", authUser, endpoint["name"]),
	}
	if len(meshSummary) > 0 {
		result["mesh"] = meshSummary
		result["text"] = fmt.Sprintf("mesh ready and linked auth_user %s to endpoint %s", authUser, endpoint["name"])
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
	text := "RelayPilot updated to " + updateVersion
	if trimmed := lastNonEmptyLines(out, 4); trimmed != "" {
		text += "\n" + trimmed
	}
	if truthy(payload["restart_services"]) {
		serviceName := firstNonEmpty(str(payload["service_name"]), envOrDefault("RELAYPILOT_AGENT_SERVICE_NAME", "relaypilot-agent"))
		if err := scheduleServiceRestart([]string{serviceName}, 12); err != nil {
			text += "\nrestart schedule failed: " + err.Error()
		} else {
			text += "\nagent service restart scheduled in ~12s"
		}
	}
	return obj{"success": true, "command": "self_update", "version": updateVersion, "restart_services": truthy(payload["restart_services"]), "text": text}
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
	maxTasks         int
	timeout          time.Duration
	topologyInterval time.Duration
	cachedTopology   obj
	cachedHealth     obj
	topologyExpires  time.Time
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
		maxTasks:         maxTasks,
		timeout:          timeout,
		topologyInterval: time.Duration(topologyIntervalSeconds) * time.Second,
	}
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

func (p *agentPoller) pollOnce() (obj, error) {
	if p.hubURL == "" || p.agentID == "" {
		return nil, errors.New("hub-url and agent-id are required")
	}
	topo, health := p.topology()
	base := strings.TrimRight(p.hubURL, "/")
	heartbeat, err := httpJSONRequestWithTLS("POST", base+"/api/agents/"+url.PathEscape(p.agentID)+"/heartbeat", obj{"topology": topo, "health": health}, p.token, p.agentID, p.timeout, p.tlsConfig)
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
	if interval < 1 {
		interval = 1
	}
	poller := newAgentPoller(hubURL, agentID, token, role, stateDir, conf, maxTasks, timeout, topologyIntervalSeconds)
	poller.tlsConfig = tlsConfig
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

type hubServer struct {
	stateDir          string
	quiet             bool
	requireClientCert bool
}

func serveHubHTTP(stateDir, host string, port int, quiet bool) error {
	return serveHub(stateDir, host, port, quiet, hubListenOptions{})
}

func serveHub(stateDir, host string, port int, quiet bool, opts hubListenOptions) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", host, port),
		Handler:           &hubServer{stateDir: stateDir, quiet: quiet, requireClientCert: opts.RequireClientCert},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	if opts.TLSCertPath != "" || opts.TLSKeyPath != "" || opts.ClientCAPath != "" || opts.RequireClientCert {
		tlsConfig, err := loadHubServerTLSConfig(opts.TLSCertPath, opts.TLSKeyPath, opts.ClientCAPath, opts.RequireClientCert)
		if err != nil {
			return err
		}
		srv.TLSConfig = tlsConfig
		fmt.Printf("RelayPilot hub api listening on https://%s:%d\n", host, port)
		return srv.ListenAndServeTLS("", "")
	}
	fmt.Printf("RelayPilot hub api listening on http://%s:%d\n", host, port)
	return srv.ListenAndServe()
}

func (s *hubServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.quiet {
		fmt.Fprintf(os.Stderr, "%s %s\n", r.Method, r.URL.RequestURI())
	}
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		writeHTTPJSON(w, 200, obj{"ok": true})
		return
	}
	body, err := readLimited(r.Body, maxHTTPBodyBytes)
	if err != nil {
		writeHTTPJSON(w, http.StatusRequestEntityTooLarge, obj{"ok": false, "error": err.Error()})
		return
	}
	parts := splitPath(r.URL.Path)
	try := func(err error) bool {
		if err != nil {
			writeHTTPJSON(w, 400, obj{"ok": false, "error": err.Error()})
			return false
		}
		return true
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "agents" && parts[3] == "heartbeat" && r.Method == "POST" {
		agentID := parts[2]
		if !s.requireAuth(w, r, body, agentID) {
			return
		}
		var payload obj
		if !try(json.Unmarshal(body, &payload)) {
			return
		}
		agent, err := updateHeartbeat(s.stateDir, agentID, asObj(payload["topology"]), asObj(payload["health"]))
		if !try(err) {
			return
		}
		writeHTTPJSON(w, 200, obj{"ok": true, "agent": agent})
		return
	}
	if len(parts) == 2 && parts[0] == "api" && parts[1] == "enroll" && r.Method == "POST" {
		var payload obj
		if !try(json.Unmarshal(body, &payload)) {
			return
		}
		enrollment, err := redeemHubEnrollCode(s.stateDir, payload)
		if !try(err) {
			return
		}
		writeHTTPJSON(w, 200, obj{"ok": true, "enrollment": enrollment})
		return
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "agents" && parts[3] == "tasks" && r.Method == "GET" {
		agentID := parts[2]
		if !s.requireAuth(w, r, body, agentID) {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 5
		}
		tasks, err := leaseHubAgentTasks(s.stateDir, agentID, limit)
		if !try(err) {
			return
		}
		writeHTTPJSON(w, 200, obj{"ok": true, "tasks": tasks})
		return
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "tasks" && parts[3] == "result" && r.Method == "POST" {
		var payload obj
		if !try(json.Unmarshal(body, &payload)) {
			return
		}
		agentID := str(payload["agent_id"])
		if !s.requireAuth(w, r, body, agentID) {
			return
		}
		task, err := completeHubTask(s.stateDir, parts[2], agentID, asObj(payload["result"]))
		if !try(err) {
			return
		}
		writeHTTPJSON(w, 200, obj{"ok": true, "task": task})
		return
	}
	writeHTTPJSON(w, 404, obj{"ok": false, "error": "not found"})
}

func (s *hubServer) requireAuth(w http.ResponseWriter, r *http.Request, body []byte, agentID string) bool {
	if r.Header.Get("X-Agent-Id") != agentID {
		writeHTTPJSON(w, 401, obj{"ok": false, "error": "agent auth id mismatch"})
		return false
	}
	if s.requireClientCert && (r.TLS == nil || len(r.TLS.PeerCertificates) == 0) {
		writeHTTPJSON(w, 401, obj{"ok": false, "error": "agent client certificate is required"})
		return false
	}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cn := r.TLS.PeerCertificates[0].Subject.CommonName
		if cn != agentID {
			writeHTTPJSON(w, 401, obj{"ok": false, "error": "agent client certificate id mismatch"})
			return false
		}
	}
	if !verifySignature(s.stateDir, agentID, r.Method, r.URL.RequestURI(), body, r.Header.Get("X-Agent-Timestamp"), r.Header.Get("X-Agent-Nonce"), r.Header.Get("X-Agent-Signature")) {
		writeHTTPJSON(w, 401, obj{"ok": false, "error": "unauthorized agent signature"})
		return false
	}
	return true
}

func splitPath(path string) []string {
	var out []string
	for _, part := range strings.Split(path, "/") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func writeHTTPJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func ensureSafeTag(value, field string) (string, error) {
	if !safeTagRE.MatchString(value) {
		return "", fmt.Errorf("%s must match %s: %q", field, safeTagRE.String(), value)
	}
	return value, nil
}

func ensurePort(v any, field string) (int, error) {
	port := int(int64Value(v))
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be in 1..65535: %v", field, v)
	}
	return port, nil
}

func generateSSPassword(method string) (string, error) {
	keyLen := ss2022KeyLengths[method]
	if keyLen == 0 {
		keyLen = 16
	}
	b := make([]byte, keyLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func validateSSPassword(method, password string, strict bool) error {
	if password == "" && method != "none" {
		return errors.New("shadowsocks password is required")
	}
	keyLen := ss2022KeyLengths[method]
	if keyLen == 0 || !strict {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(password)
	if err != nil {
		return fmt.Errorf("%s password must be base64 for %d bytes", method, keyLen)
	}
	if len(raw) != keyLen {
		return fmt.Errorf("%s password must decode to %d bytes, got %d", method, keyLen, len(raw))
	}
	return nil
}

func validateEndpoint(endpoint obj, strictPassword bool) (obj, error) {
	if str(endpoint["kind"]) != endpointKind {
		return nil, fmt.Errorf("unsupported endpoint kind: %q", endpoint["kind"])
	}
	if int64Value(endpoint["version"]) != version {
		return nil, fmt.Errorf("unsupported endpoint version: %v", endpoint["version"])
	}
	name, err := ensureSafeName(str(endpoint["name"]), "endpoint.name")
	if err != nil {
		return nil, err
	}
	tag, err := ensureSafeTag(str(endpoint["tag"]), "endpoint.tag")
	if err != nil {
		return nil, err
	}
	out := obj{}
	for k, v := range endpoint {
		out[k] = v
	}
	out["name"] = name
	out["tag"] = tag
	protocol := str(out["protocol"])
	switch protocol {
	case "shadowsocks":
		for _, key := range []string{"server", "server_port", "method", "password"} {
			if str(out[key]) == "" {
				return nil, fmt.Errorf("missing shadowsocks endpoint field: %s", key)
			}
		}
		port, err := ensurePort(out["server_port"], "endpoint.server_port")
		if err != nil {
			return nil, err
		}
		out["server_port"] = port
		method := str(out["method"])
		if !ssMethods[method] {
			return nil, fmt.Errorf("unsupported shadowsocks method: %s", method)
		}
		if err := validateSSPassword(method, str(out["password"]), strictPassword); err != nil {
			return nil, err
		}
		if network := str(out["network"]); network != "" {
			if network != "tcp" && network != "udp" && network != "tcp,udp" && network != "udp,tcp" {
				return nil, errors.New("endpoint.network must be tcp, udp, tcp,udp, or omitted")
			}
		}
		return out, nil
	case "socks":
		for _, key := range []string{"server", "server_port"} {
			if str(out[key]) == "" {
				return nil, fmt.Errorf("missing socks endpoint field: %s", key)
			}
		}
		port, err := ensurePort(out["server_port"], "endpoint.server_port")
		if err != nil {
			return nil, err
		}
		out["server_port"] = port
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported endpoint protocol: %q", protocol)
	}
}

func makeSSEndpoint(name, server string, serverPort int, method, password, tag, network, description string) (obj, error) {
	if tag == "" {
		tag = "landing-" + name + "-ss"
	}
	endpoint := obj{"kind": endpointKind, "version": version, "name": name, "protocol": "shadowsocks", "server": server, "server_port": serverPort, "method": method, "password": password, "tag": tag}
	if network != "" {
		endpoint["network"] = network
	}
	if description != "" {
		endpoint["description"] = description
	}
	return validateEndpoint(endpoint, true)
}

func makeLandingSSConfig(listen string, listenPort int, method, password, tag string) (obj, error) {
	if err := validateSSPassword(method, password, true); err != nil {
		return nil, err
	}
	if _, err := ensureSafeTag(tag, "inbound.tag"); err != nil {
		return nil, err
	}
	port, err := ensurePort(listenPort, "listen_port")
	if err != nil {
		return nil, err
	}
	return obj{"log": obj{"level": "info", "timestamp": true}, "inbounds": []any{obj{"type": "shadowsocks", "tag": tag, "listen": listen, "listen_port": port, "method": method, "password": password}}, "outbounds": []any{obj{"type": "direct", "tag": "direct"}}, "route": obj{"final": "direct"}}, nil
}

func renderLandingSS(name, server, listen string, listenPort, serverPort int, method, password, network, inboundTag, endpointTag string) (obj, obj, error) {
	endpoint, err := makeSSEndpoint(name, server, serverPort, method, password, endpointTag, network, name+" landing via sing-box Shadowsocks")
	if err != nil {
		return nil, nil, err
	}
	config, err := makeLandingSSConfig(listen, listenPort, method, password, inboundTag)
	if err != nil {
		return nil, nil, err
	}
	return endpoint, config, nil
}

func generateRealityKeypair() (string, string, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.Bytes()), base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func validateRealityPrivateKey(privateKey string) (string, string, error) {
	privateKey = strings.TrimSpace(privateKey)
	if privateKey == "" {
		return "", "", errors.New("reality private_key is required")
	}
	raw, err := base64.RawURLEncoding.DecodeString(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("reality private_key must be raw URL base64: %w", err)
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid reality private_key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(key.Bytes()), base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func validateRealityShortID(shortID string) (string, error) {
	shortID = strings.ToLower(strings.TrimSpace(shortID))
	if shortID == "" {
		return "", errors.New("reality short_id is required")
	}
	if len(shortID) > 16 || len(shortID)%2 != 0 {
		return "", errors.New("reality short_id must be 2 to 16 hex characters")
	}
	if _, err := hex.DecodeString(shortID); err != nil {
		return "", errors.New("reality short_id must be hexadecimal")
	}
	return shortID, nil
}

func newRealityShortID() string {
	return mustRandomHex(8)
}

func getRealityShortID(inbound obj) string {
	tlsCfg := asObj(inbound["tls"])
	reality := asObj(tlsCfg["reality"])
	shortIDs := asList(reality["short_id"])
	if len(shortIDs) > 0 {
		return str(shortIDs[0])
	}
	return str(reality["short_id"])
}

func makeTransitRealityInbound(listen string, listenPort int, inboundTag, serverName, handshakeServer string, handshakePort int, privateKey, shortID, maxTimeDifference string) (obj, string, error) {
	if _, err := ensureSafeTag(inboundTag, "inbound.tag"); err != nil {
		return nil, "", err
	}
	port, err := ensurePort(listenPort, "listen_port")
	if err != nil {
		return nil, "", err
	}
	handshakePort, err = ensurePort(handshakePort, "handshake_port")
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(serverName) == "" {
		return nil, "", errors.New("server_name is required")
	}
	if strings.TrimSpace(handshakeServer) == "" {
		return nil, "", errors.New("handshake_server is required")
	}
	var publicKey string
	if privateKey == "" {
		privateKey, publicKey, err = generateRealityKeypair()
		if err != nil {
			return nil, "", err
		}
	} else {
		privateKey, publicKey, err = validateRealityPrivateKey(privateKey)
		if err != nil {
			return nil, "", err
		}
	}
	if shortID == "" {
		shortID = newRealityShortID()
	} else if shortID, err = validateRealityShortID(shortID); err != nil {
		return nil, "", err
	}
	reality := obj{
		"enabled":     true,
		"handshake":   obj{"server": handshakeServer, "server_port": handshakePort},
		"private_key": privateKey,
		"short_id":    []any{shortID},
	}
	if strings.TrimSpace(maxTimeDifference) != "" {
		reality["max_time_difference"] = maxTimeDifference
	}
	inbound := obj{
		"type":        "vless",
		"tag":         inboundTag,
		"listen":      listen,
		"listen_port": port,
		"users":       []any{},
		"tls": obj{
			"enabled":     true,
			"server_name": serverName,
			"reality":     reality,
		},
	}
	return inbound, publicKey, nil
}

func loadEndpoint(path string, strictPassword bool) (obj, error) {
	data, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	return validateEndpoint(data, strictPassword)
}

func endpointStorePath(stateDir, name string) (string, error) {
	name, err := ensureSafeName(name, "endpoint.name")
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "endpoints", name+".json"), nil
}

func importEndpoint(endpointPath, stateDir string) (string, error) {
	endpoint, err := loadEndpoint(endpointPath, true)
	if err != nil {
		return "", err
	}
	dest, err := endpointStorePath(stateDir, str(endpoint["name"]))
	if err != nil {
		return "", err
	}
	return dest, writeJSON(dest, endpoint, 0o600)
}

func exportEndpoint(stateDir, name string) (obj, error) {
	path, err := endpointStorePath(stateDir, name)
	if err != nil {
		return nil, err
	}
	return loadEndpoint(path, true)
}

func endpointToOutbound(endpoint obj) (obj, error) {
	endpoint, err := validateEndpoint(endpoint, true)
	if err != nil {
		return nil, err
	}
	switch str(endpoint["protocol"]) {
	case "shadowsocks":
		out := obj{"type": "shadowsocks", "tag": endpoint["tag"], "server": endpoint["server"], "server_port": endpoint["server_port"], "method": endpoint["method"], "password": endpoint["password"]}
		if str(endpoint["network"]) != "" {
			out["network"] = endpoint["network"]
		}
		return out, nil
	case "socks":
		versionField := firstNonNil(endpoint["version_field"], endpoint["socks_version"], "5")
		out := obj{"type": "socks", "tag": endpoint["tag"], "server": endpoint["server"], "server_port": endpoint["server_port"], "version": str(versionField)}
		if str(endpoint["username"]) != "" {
			out["username"] = endpoint["username"]
		}
		if str(endpoint["password"]) != "" {
			out["password"] = endpoint["password"]
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported endpoint protocol: %q", endpoint["protocol"])
	}
}

type configFile struct {
	path     string
	data     obj
	modified bool
}

func loadConfigFiles(conf string) ([]*configFile, error) {
	info, err := os.Stat(conf)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if strings.HasSuffix(conf, ".json") {
				return []*configFile{{path: conf, data: obj{}, modified: true}}, nil
			}
			return []*configFile{}, nil
		}
		return nil, err
	}
	var paths []string
	if info.IsDir() {
		entries, err := os.ReadDir(conf)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				paths = append(paths, filepath.Join(conf, e.Name()))
			}
		}
		sort.Strings(paths)
	} else {
		paths = []string{conf}
	}
	if len(paths) == 0 {
		return []*configFile{}, nil
	}
	files := make([]*configFile, 0, len(paths))
	for _, path := range paths {
		data, err := loadJSON(path)
		if err != nil {
			return nil, err
		}
		files = append(files, &configFile{path: path, data: data})
	}
	return files, nil
}

func saveConfigFiles(files []*configFile) error {
	for _, file := range files {
		if file.modified {
			if err := writeJSON(file.path, file.data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func backupConfigIfExists(conf, stateDir string) (string, error) {
	if _, err := os.Stat(conf); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return backupConfig(conf, stateDir)
}

func backupConfig(conf, stateDir string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	backupRoot := filepath.Join(filepath.Dir(conf))
	baseName := filepath.Base(conf) + ".bak.relaypilot-" + stamp
	if stateDir != "" {
		backupRoot = filepath.Join(stateDir, "backups")
		baseName = filepath.Base(conf) + ".bak." + stamp
	}
	base := filepath.Join(backupRoot, baseName)
	dest := base
	for i := 2; ; i++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		dest = fmt.Sprintf("%s.%d", base, i)
	}
	info, err := os.Stat(conf)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dest, copyDir(conf, dest)
	}
	return dest, copyFile(conf, dest, info.Mode().Perm())
}

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func findVlessInbound(files []*configFile, inboundTag string) (*configFile, obj, error) {
	var firstFile *configFile
	var firstInbound obj
	for _, file := range files {
		for _, raw := range asList(file.data["inbounds"]) {
			inbound := asObj(raw)
			if str(inbound["type"]) != "vless" {
				continue
			}
			if firstInbound == nil {
				firstFile, firstInbound = file, inbound
			}
			if inboundTag != "" && str(inbound["tag"]) == inboundTag {
				return file, inbound, nil
			}
		}
	}
	if inboundTag != "" {
		return nil, nil, fmt.Errorf("VLESS inbound not found: %s", inboundTag)
	}
	if firstInbound != nil {
		return firstFile, firstInbound, nil
	}
	return nil, nil, errors.New("no VLESS inbound found")
}

func ensureInboundsFile(files *[]*configFile, conf string) *configFile {
	for _, file := range *files {
		if asList(file.data["inbounds"]) != nil {
			return file
		}
	}
	if info, err := os.Stat(conf); err == nil && !info.IsDir() && len(*files) > 0 {
		file := (*files)[0]
		file.data["inbounds"] = []any{}
		file.modified = true
		return file
	}
	path := conf
	if !strings.HasSuffix(conf, ".json") {
		path = filepath.Join(conf, "00-relaypilot-reality.json")
	}
	file := &configFile{path: path, data: obj{"inbounds": []any{}}, modified: true}
	*files = append(*files, file)
	return file
}

func ensureTransitReality(conf, stateDir, listen string, listenPort int, inboundTag, serverName, handshakeServer string, handshakePort int, privateKey, shortID, maxTimeDifference string, doBackup bool) (obj, error) {
	if inboundTag == "" {
		inboundTag = "vless-in"
	}
	if _, err := ensureSafeTag(inboundTag, "inbound_tag"); err != nil {
		return nil, err
	}
	if serverName == "" {
		serverName = "www.cloudflare.com"
	}
	if handshakeServer == "" {
		handshakeServer = serverName
	}
	files, err := loadConfigFiles(conf)
	if err != nil {
		return nil, err
	}
	backupPath := ""
	if doBackup {
		backupPath, err = backupConfigIfExists(conf, stateDir)
		if err != nil {
			return nil, err
		}
	}
	if inboundFile, inbound, err := findVlessInbound(files, inboundTag); err == nil {
		listenPort, err = ensurePort(listenPort, "listen_port")
		if err != nil {
			return nil, err
		}
		handshakePort, err = ensurePort(handshakePort, "handshake_port")
		if err != nil {
			return nil, err
		}
		tlsCfg := asObj(inbound["tls"])
		reality := asObj(tlsCfg["reality"])
		if len(reality) == 0 {
			reality = obj{}
		}
		publicKey := ""
		if privateKey == "" {
			if existingKey := str(reality["private_key"]); existingKey != "" {
				privateKey = existingKey
			}
		}
		if privateKey == "" {
			privateKey, publicKey, err = generateRealityKeypair()
			if err != nil {
				return nil, err
			}
		} else {
			privateKey, publicKey, err = validateRealityPrivateKey(privateKey)
			if err != nil {
				return nil, err
			}
		}
		if shortID == "" {
			shortID = getRealityShortID(inbound)
		}
		if shortID == "" {
			shortID = newRealityShortID()
		} else if shortID, err = validateRealityShortID(shortID); err != nil {
			return nil, err
		}
		inbound["type"] = "vless"
		inbound["tag"] = inboundTag
		inbound["listen"] = listen
		inbound["listen_port"] = listenPort
		if asList(inbound["users"]) == nil {
			inbound["users"] = []any{}
		}
		tlsCfg["enabled"] = true
		tlsCfg["server_name"] = serverName
		reality["enabled"] = true
		reality["handshake"] = obj{"server": handshakeServer, "server_port": handshakePort}
		reality["private_key"] = privateKey
		reality["short_id"] = []any{shortID}
		if strings.TrimSpace(maxTimeDifference) != "" {
			reality["max_time_difference"] = maxTimeDifference
		}
		tlsCfg["reality"] = reality
		inbound["tls"] = tlsCfg
		inboundFile.modified = true
		if err := saveConfigFiles(files); err != nil {
			return nil, err
		}
		return obj{"conf": conf, "backup": backupPath, "inbound_file": inboundFile.path, "inbound_tag": inboundTag, "listen": listen, "listen_port": listenPort, "server_name": serverName, "handshake_server": handshakeServer, "handshake_port": handshakePort, "public_key": publicKey, "short_id": shortID, "updated": true}, nil
	}
	inbound, publicKey, err := makeTransitRealityInbound(listen, listenPort, inboundTag, serverName, handshakeServer, handshakePort, privateKey, shortID, maxTimeDifference)
	if err != nil {
		return nil, err
	}
	inboundFile := ensureInboundsFile(&files, conf)
	inbounds := asList(inboundFile.data["inbounds"])
	inboundFile.data["inbounds"] = append(inbounds, inbound)
	inboundFile.modified = true
	if err := saveConfigFiles(files); err != nil {
		return nil, err
	}
	return obj{"conf": conf, "backup": backupPath, "inbound_file": inboundFile.path, "inbound_tag": inboundTag, "listen": listen, "listen_port": listenPort, "server_name": serverName, "handshake_server": handshakeServer, "handshake_port": handshakePort, "public_key": publicKey, "short_id": getRealityShortID(inbound), "updated": false}, nil
}

func ensureOutboundsFile(files *[]*configFile, conf string) *configFile {
	for _, file := range *files {
		if asList(file.data["outbounds"]) != nil {
			return file
		}
	}
	if info, err := os.Stat(conf); err == nil && !info.IsDir() {
		file := ensureInboundsFile(files, conf)
		file.data["outbounds"] = []any{}
		file.modified = true
		return file
	}
	file := &configFile{path: filepath.Join(conf, "90-relaypilot-outbounds.json"), data: obj{"outbounds": []any{}}, modified: true}
	*files = append(*files, file)
	return file
}

func ensureRouteFile(files *[]*configFile, conf string) *configFile {
	for _, file := range *files {
		if route := asObj(file.data["route"]); len(route) > 0 {
			if asList(route["rules"]) == nil {
				route["rules"] = []any{}
				file.modified = true
			}
			file.data["route"] = route
			return file
		}
	}
	if info, err := os.Stat(conf); err == nil && !info.IsDir() {
		file := (*files)[0]
		file.data["route"] = obj{"rules": []any{}}
		file.modified = true
		return file
	}
	file := &configFile{path: filepath.Join(conf, "91-relaypilot-route.json"), data: obj{"route": obj{"rules": []any{}}}, modified: true}
	*files = append(*files, file)
	return file
}

func upsertOutbound(outbounds []any, outbound obj) []any {
	tag := str(outbound["tag"])
	for idx, raw := range outbounds {
		if str(asObj(raw)["tag"]) == tag {
			outbounds[idx] = outbound
			return outbounds
		}
	}
	return append(outbounds, outbound)
}

func upsertVlessUser(inbound obj, name, uuid, flow string) error {
	if _, err := ensureSafeName(name, "auth_user"); err != nil {
		return err
	}
	users := asList(inbound["users"])
	if users == nil {
		users = []any{}
	}
	for _, raw := range users {
		user := asObj(raw)
		if str(user["name"]) == name {
			user["uuid"] = uuid
			if flow != "" {
				user["flow"] = flow
			}
			inbound["users"] = users
			return nil
		}
	}
	for _, raw := range users {
		user := asObj(raw)
		if str(user["uuid"]) == uuid {
			if str(user["name"]) == "" {
				user["name"] = name
			}
			if flow != "" && str(user["flow"]) == "" {
				user["flow"] = flow
			}
			inbound["users"] = users
			return nil
		}
	}
	newUser := obj{"name": name, "uuid": uuid}
	if flow != "" {
		newUser["flow"] = flow
	}
	inbound["users"] = append(users, newUser)
	return nil
}

func routeMatches(rule obj, inboundTag, authUser string) bool {
	return listContains(rule["inbound"], inboundTag) && listContains(rule["auth_user"], authUser)
}

func routeReferencesOutbound(rule obj, outboundTag string) bool {
	return outboundTag != "" && str(rule["outbound"]) == outboundTag
}

func listContains(v any, target string) bool {
	for _, item := range normalizeList(v) {
		if str(item) == target {
			return true
		}
	}
	return false
}

func removeVlessUser(inbound obj, authUser string) bool {
	if authUser == "" {
		return false
	}
	users := asList(inbound["users"])
	if users == nil {
		return false
	}
	filtered := []any{}
	removed := false
	for _, raw := range users {
		user := asObj(raw)
		if str(user["name"]) == authUser {
			removed = true
			continue
		}
		filtered = append(filtered, raw)
	}
	if removed {
		inbound["users"] = filtered
	}
	return removed
}

func removeOutboundByTag(outbounds []any, outboundTag string) ([]any, bool) {
	if outboundTag == "" {
		return outbounds, false
	}
	filtered := []any{}
	removed := false
	for _, raw := range outbounds {
		if str(asObj(raw)["tag"]) == outboundTag {
			removed = true
			continue
		}
		filtered = append(filtered, raw)
	}
	return filtered, removed
}

func routeStillReferences(rules []any, outboundTag string) bool {
	for _, raw := range rules {
		if routeReferencesOutbound(asObj(raw), outboundTag) {
			return true
		}
	}
	return false
}

func removeEndpointFile(stateDir, endpointName string) (bool, error) {
	if endpointName == "" {
		return false, nil
	}
	path, err := endpointStorePath(stateDir, endpointName)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func unbindTransit(conf, stateDir, inboundTag, authUser, endpointName, outboundTag string, teardownMesh bool, mesh obj, doBackup bool) (obj, error) {
	if authUser != "" {
		if _, err := ensureSafeName(authUser, "auth_user"); err != nil {
			return nil, err
		}
	}
	if endpointName != "" {
		if _, err := ensureSafeName(endpointName, "endpoint_name"); err != nil {
			return nil, err
		}
	}
	endpoint := obj{}
	if endpointName != "" {
		if loaded, err := exportEndpoint(stateDir, endpointName); err == nil {
			endpoint = loaded
			if outboundTag == "" {
				outboundTag = str(endpoint["tag"])
			}
			if len(mesh) == 0 {
				mesh = asObj(endpoint["mesh"])
			}
		}
	}
	if outboundTag != "" {
		if _, err := ensureSafeTag(outboundTag, "outbound_tag"); err != nil {
			return nil, err
		}
	}
	files, err := loadConfigFiles(conf)
	if err != nil {
		return nil, err
	}
	backupPath := ""
	if doBackup {
		backupPath, err = backupConfigIfExists(conf, stateDir)
		if err != nil {
			return nil, err
		}
	}
	authUsersToRemove := map[string]bool{}
	if authUser != "" {
		authUsersToRemove[authUser] = true
	}
	removedRoutes := 0
	for _, file := range files {
		route := asObj(file.data["route"])
		if len(route) == 0 {
			continue
		}
		rules := asList(route["rules"])
		filtered := []any{}
		fileRemovedRoutes := 0
		for _, raw := range rules {
			rule := asObj(raw)
			matchUser := authUser != "" && listContains(rule["auth_user"], authUser)
			matchOutbound := outboundTag != "" && routeReferencesOutbound(rule, outboundTag)
			if len(rule) > 0 && (matchUser || matchOutbound) {
				for _, removedUser := range normalizeList(rule["auth_user"]) {
					if name := str(removedUser); name != "" {
						authUsersToRemove[name] = true
					}
				}
				removedRoutes++
				fileRemovedRoutes++
				continue
			}
			filtered = append(filtered, raw)
		}
		if fileRemovedRoutes > 0 {
			route["rules"] = filtered
			file.data["route"] = route
			file.modified = true
		}
	}
	removedUsers := 0
	for _, file := range files {
		for _, raw := range asList(file.data["inbounds"]) {
			inbound := asObj(raw)
			if str(inbound["type"]) != "vless" {
				continue
			}
			if inboundTag != "" && str(inbound["tag"]) != inboundTag {
				continue
			}
			for name := range authUsersToRemove {
				if removeVlessUser(inbound, name) {
					removedUsers++
					file.modified = true
				}
			}
		}
	}
	allRules := []any{}
	for _, file := range files {
		allRules = append(allRules, asList(asObj(file.data["route"])["rules"])...)
	}
	removedOutbounds := 0
	if outboundTag != "" && !routeStillReferences(allRules, outboundTag) {
		for _, file := range files {
			outbounds := asList(file.data["outbounds"])
			if outbounds == nil {
				continue
			}
			filtered, removed := removeOutboundByTag(outbounds, outboundTag)
			if removed {
				removedOutbounds++
				file.data["outbounds"] = filtered
				file.modified = true
			}
		}
	}
	if err := saveConfigFiles(files); err != nil {
		return nil, err
	}
	endpointRemoved, err := removeEndpointFile(stateDir, endpointName)
	if err != nil {
		return nil, err
	}
	meshSummary := obj{}
	if teardownMesh {
		iface := str(mesh["interface"])
		if iface != "" {
			meshSummary, err = teardownWireGuardMesh(firstNonEmpty(str(mesh["config_dir"]), defaultMeshConfigDir), iface)
			if err != nil {
				return nil, err
			}
		}
	}
	return obj{
		"backup":            backupPath,
		"conf":              conf,
		"endpoint_name":     endpointName,
		"outbound_tag":      outboundTag,
		"auth_user":         authUser,
		"removed_users":     removedUsers,
		"removed_routes":    removedRoutes,
		"removed_outbounds": removedOutbounds,
		"removed_endpoint":  endpointRemoved,
		"mesh":              meshSummary,
	}, nil
}

func teardownWireGuardMesh(configDir, iface string) (obj, error) {
	iface, err := ensureWGInterface(iface, "mesh.interface")
	if err != nil {
		return nil, err
	}
	if configDir == "" {
		configDir = defaultMeshConfigDir
	}
	if !filepath.IsAbs(configDir) {
		return nil, errors.New("mesh.config_dir must be absolute")
	}
	configPath := filepath.Join(configDir, iface+".conf")
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return obj{"interface": iface, "config": configPath, "state": "absent"}, nil
	}
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(data, []byte(wireGuardConfigMarker)) {
		return nil, fmt.Errorf("refusing to remove non-RelayPilot WireGuard config: %s", configPath)
	}
	downState := "skipped"
	if _, err := exec.LookPath("wg-quick"); err == nil {
		if err := runCommand(15*time.Second, "wg-quick", "down", configPath); err != nil {
			downState = "failed: " + err.Error()
		} else {
			downState = "down"
		}
	} else {
		downState = "wg-quick-missing"
	}
	if err := os.Remove(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return obj{"interface": iface, "config": configPath, "state": "removed", "down": downState}, nil
}

func bindTransit(conf string, endpoint obj, inboundTag, authUser, clientUUID, flow string, setFinalDirect bool, stateDir string, doBackup bool) (obj, error) {
	endpoint, err := validateEndpoint(endpoint, true)
	if err != nil {
		return nil, err
	}
	if _, err := ensureSafeName(authUser, "auth_user"); err != nil {
		return nil, err
	}
	if clientUUID == "" {
		clientUUID = generateUUID()
	}
	files, err := loadConfigFiles(conf)
	if err != nil {
		return nil, err
	}
	backupPath := ""
	if doBackup {
		backupPath, err = backupConfigIfExists(conf, stateDir)
		if err != nil {
			return nil, err
		}
	}
	inboundFile, inbound, err := findVlessInbound(files, inboundTag)
	if err != nil {
		return nil, err
	}
	actualInboundTag := str(firstNonNil(inbound["tag"], inboundTag, "vless-in"))
	if err := upsertVlessUser(inbound, authUser, clientUUID, flow); err != nil {
		return nil, err
	}
	inboundFile.modified = true
	outbound, err := endpointToOutbound(endpoint)
	if err != nil {
		return nil, err
	}
	outboundFile := ensureOutboundsFile(&files, conf)
	outbounds := asList(outboundFile.data["outbounds"])
	outboundFile.data["outbounds"] = upsertOutbound(outbounds, outbound)
	outboundFile.modified = true
	routeFile := ensureRouteFile(&files, conf)
	route := asObj(routeFile.data["route"])
	rules := asList(route["rules"])
	filtered := []any{}
	for _, raw := range rules {
		rule := asObj(raw)
		if len(rule) > 0 && routeMatches(rule, actualInboundTag, authUser) {
			continue
		}
		filtered = append(filtered, raw)
	}
	newRule := obj{"inbound": []any{actualInboundTag}, "auth_user": []any{authUser}, "outbound": endpoint["tag"], "action": "route"}
	route["rules"] = append([]any{newRule}, filtered...)
	if setFinalDirect {
		route["final"] = "direct"
	}
	routeFile.data["route"] = route
	routeFile.modified = true
	if err := saveConfigFiles(files); err != nil {
		return nil, err
	}
	return obj{"backup": backupPath, "conf": conf, "inbound_file": inboundFile.path, "outbound_file": outboundFile.path, "route_file": routeFile.path, "inbound_tag": actualInboundTag, "auth_user": authUser, "client_uuid": clientUUID, "outbound_tag": endpoint["tag"], "endpoint_name": endpoint["name"]}, nil
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
