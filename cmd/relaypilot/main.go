package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
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
	hubExportsDirName              = "hub-exports"
	hubAgentTokensName             = "hub-agent-tokens.json"
	hubAuthNoncesName              = "hub-auth-nonces.json"
	hubEnrollCodesName             = "hub-enroll-codes.json"
	hubTLSDirName                  = "hub-tls"
	hubTLSCACertName               = "ca.crt"
	hubTLSCAKeyName                = "ca.key"
	hubTLSServerCertName           = "hub.crt"
	hubTLSServerKeyName            = "hub.key"
	agentEnrollmentName            = "agent-enrollment.json"
	agentPolicyName                = "agent-policy.json"
	publicEntriesName              = "public-entries.json"
	defaultStateDir                = "/etc/relaypilot"
	defaultConfDir                 = "/etc/sing-box/conf"
	defaultHubTLSDays              = 3650
	defaultAgentCertDays           = 1095
	defaultEnrollCodeTTLSeconds    = 600
	defaultPublicIPIntervalSeconds = 600
	defaultPublicIPProbeTimeout    = 3 * time.Second
	defaultGeoIPLookupTimeout      = 2 * time.Second
	geoIPLocationRefreshSeconds    = 604800
	geoIPLocationRetrySeconds      = 86400
	defaultLinkProbeTimeoutSeconds = 3
	maxLinkProbeTimeoutSeconds     = 5
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
	hubServiceRestartDelaySeconds  = 5
	agentServiceRestartDelaySecs   = 12
	defaultTaskLeaseTimeoutSeconds = 120
	defaultTaskMaxLeaseCount       = 3
	defaultTaskRetentionSeconds    = 7 * 86400
	defaultMaxCompletedHubTasks    = 500
	defaultTaskPruneIntervalSecs   = 3600
	maxStoredTaskStringBytes       = 32 << 10
	defaultMeshConfigDir           = "/etc/wireguard"
	defaultMeshKeepaliveSeconds    = 25
	defaultSyncStaleSeconds        = 21600
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
	{"command": "relaypilot", "args": "", "description": "Open RelayPilot Hub panel", "scope": "read"},
}

var hubTelegramHelpRows = append(cloneObjList(hubTelegramCommandRows),
	obj{"command": "relaypilot_panel", "args": "", "description": "Open Hub control panel alias", "scope": "read"},
	obj{"command": "relaypilot_status", "args": "[hub|all|transit|landing|agent_id]", "description": "Show hub status or queue status checks for selected agents", "scope": "read"},
	obj{"command": "relaypilot_help", "args": "", "description": "Show hub command syntax", "scope": "read"},
	obj{"command": "relaypilot_agents", "args": "", "description": "List registered transit and landing agents", "scope": "read"},
	obj{"command": "relaypilot_topology", "args": "", "description": "Show transit to landing tree view", "scope": "read"},
	obj{"command": "relaypilot_doctor", "args": "[hub|all|agent_id]", "description": "Show hub health or queue agent doctor checks", "scope": "read"},
	obj{"command": "relaypilot_endpoints", "args": "[all|transit|landing|agent_id]", "description": "Queue endpoint listing for selected agents", "scope": "read"},
	obj{"command": "relaypilot_show_endpoint", "args": "<agent_id> <endpoint_name>", "description": "Queue endpoint detail request for one agent", "scope": "read"},
	obj{"command": "relaypilot_inspect_conf", "args": "<agent_id> [path]", "description": "Queue config inspection for one agent", "scope": "read"},
	obj{"command": "relaypilot_link", "args": "<transit_id> <landing_id> [auth_user] [endpoint_name] [--mode direct|mesh]", "description": "Link one transit to one landing endpoint", "scope": "write"},
	obj{"command": "relaypilot_probe", "args": "<transit_id> <landing_id> [endpoint_name] [--timeout seconds]", "description": "Run one on-demand low-resource TCP link probe", "scope": "read"},
	obj{"command": "relaypilot_update", "args": "<hub|all|transit|landing|agent_id> <version|latest> [--restart]", "description": "Update Hub or queue node self-update tasks", "scope": "write"},
	obj{"command": "relaypilot_decommission", "args": "<agent_id> [--mode detach|purge-managed-proxy|uninstall] [--confirm agent_id]", "description": "Queue remote agent retirement; dry-run unless exact confirm is provided", "scope": "write"},
	obj{"command": "relaypilot_tasks", "args": "", "description": "List queued hub tasks", "scope": "read"},
	obj{"command": "relaypilot_results", "args": "[batch_id]", "description": "Show completed hub task results", "scope": "read"},
	obj{"command": "relaypilot_alerts", "args": "", "description": "List offline-node alerts", "scope": "read"},
)

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
	case "render-landing-socks":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		name := fs.String("name", "", "endpoint name")
		server := fs.String("server", "", "client-visible server")
		listen := fs.String("listen", "::", "listen address")
		listenPort := fs.Int("listen-port", 0, "listen port")
		serverPort := fs.Int("server-port", 0, "server port")
		username := fs.String("username", "", "SOCKS username")
		password := fs.String("password", "", "SOCKS password")
		inboundTag := fs.String("inbound-tag", "socks-in", "inbound tag")
		endpointTag := fs.String("endpoint-tag", "", "endpoint/outbound tag")
		configOutput := fs.String("config-output", "", "config output path")
		endpointOutput := fs.String("endpoint-output", "", "endpoint output path")
		_ = fs.Parse(args[1:])
		if *serverPort == 0 {
			*serverPort = *listenPort
		}
		endpoint, config, err := renderLandingSOCKS(*name, *server, *listen, *listenPort, *serverPort, *username, *password, *inboundTag, *endpointTag)
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
	case "agent-connection-info":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		conf := fs.String("conf", defaultConfDir, "sing-box config file or directory")
		authUser := fs.String("auth-user", "", "filter Reality client by auth_user")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		info, err := agentConnectionInfo(*stateDir, *conf, *authUser)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(info)
		}
		fmt.Println(formatAgentConnectionInfoText(info))
		return nil
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
	case "public-entry-set":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		use := fs.String("use", "", "entry use: reality, shadowsocks, or wireguard")
		name := fs.String("name", "", "entry name")
		host := fs.String("host", "", "public host or domain")
		publicPort := fs.Int("public-port", 0, "public port")
		localPort := fs.Int("local-port", 0, "local listen port")
		network := fs.String("network", "", "tcp or udp")
		_ = fs.Parse(args[1:])
		res, err := setPublicEntry(*stateDir, publicEntryOptions{Use: *use, Name: *name, Host: *host, PublicPort: *publicPort, LocalPort: *localPort, Network: *network})
		if err != nil {
			return err
		}
		return printJSON(res)
	case "public-entry-list":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args[1:])
		entries, err := listPublicEntries(*stateDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(obj{"state_dir": *stateDir, "entries": entries})
		}
		fmt.Println(formatPublicEntriesText(entries))
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
		role := fs.String("role", "", "agent role; defaults to existing agent role or transit")
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
		asText := fs.Bool("text", false, "print human-readable invite")
		_ = fs.Parse(args[1:])
		resolvedHubURL, err := resolveHubPublicURL(hubPublicURLOptions{ExplicitURL: *hubURL, PublicHost: *publicHost, Port: *publicPort}, detectPublicIP)
		if err != nil {
			return err
		}
		res, err := createHubEnrollInvite(*stateDir, hubEnrollCodeOptions{HubURL: resolvedHubURL, AgentID: *agentID, Role: *role, Name: *name, Labels: *labels, TTLSeconds: int64(ttl.Seconds()), Days: *days})
		if err != nil {
			return err
		}
		if *asText {
			fmt.Println(formatHubEnrollInviteText(res))
			return nil
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
	case "hub-export-client":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		transitID := fs.String("transit-id", "", "transit agent id")
		authUser := fs.String("auth-user", "", "linked auth_user")
		format := fs.String("format", "remnawave", "output format: remnawave or json")
		_ = fs.Parse(args[1:])
		exported, err := hubExportTransitClient(*stateDir, *transitID, *authUser)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(*format)) {
		case "", "remnawave", "json":
			return printJSON(exported)
		default:
			return fmt.Errorf("unsupported export format: %s", *format)
		}
	case "hub-export-landing":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		landingID := fs.String("landing-id", "", "landing agent id")
		endpointName := fs.String("endpoint-name", "", "landing endpoint name")
		_ = fs.Parse(args[1:])
		exported, err := hubExportLandingConfig(*stateDir, *landingID, *endpointName)
		if err != nil {
			return err
		}
		return printJSON(exported)
	case "hub-sync-agent":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		agentID := fs.String("agent-id", "", "agent id")
		_ = fs.Parse(args[1:])
		res, err := hubSyncAgent(*stateDir, *agentID)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "hub-sync-all":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		_ = fs.Parse(args[1:])
		res, err := hubSyncAll(*stateDir)
		if err != nil {
			return err
		}
		return printJSON(res)
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
		ipMode := fs.String("ip-mode", "", "agent IP mode: static or dynamic")
		publicIPInterval := fs.Int("public-ip-interval", 0, "seconds between public IP probes in dynamic mode")
		maxTasks := fs.Int("max-tasks", 5, "max tasks")
		topologyInterval := fs.Int("topology-interval", 0, "reuse topology snapshot for this many seconds; 0 disables cache")
		timeout := fs.Int("timeout", 10, "HTTP timeout seconds")
		_ = fs.Parse(args[1:])
		_ = *name
		if _, err := parseLabels(*labels); err != nil {
			return err
		}
		if err := applyAgentEnrollmentDefaults(*enrollmentFile, hubURL, agentID, role, tokenFile, caCert, clientCert, clientKey, ipMode, publicIPInterval); err != nil {
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
		if err := configurePollerNetwork(poller, *ipMode, *publicIPInterval); err != nil {
			return err
		}
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
		ipMode := fs.String("ip-mode", "", "agent IP mode: static or dynamic")
		publicIPInterval := fs.Int("public-ip-interval", 0, "seconds between public IP probes in dynamic mode")
		interval := fs.Int("interval", 30, "poll interval seconds")
		maxTasks := fs.Int("max-tasks", 5, "max tasks")
		topologyInterval := fs.Int("topology-interval", defaultTopologyInterval, "reuse topology snapshot for this many seconds")
		timeout := fs.Int("timeout", 10, "HTTP timeout seconds")
		_ = fs.Parse(args[1:])
		_ = *name
		if _, err := parseLabels(*labels); err != nil {
			return err
		}
		if *enrollmentFile != "" &&
			*hubURL == "" && *agentID == "" && *role == "" &&
			*token == "" && *tokenFile == "" &&
			*caCert == "" && *clientCert == "" && *clientKey == "" && *tlsServerName == "" &&
			*ipMode == "" && *publicIPInterval == 0 {
			return agentPollLoopWithEnrollmentFile(*enrollmentFile, *stateDir, *conf, *interval, *maxTasks, time.Duration(*timeout)*time.Second, *topologyInterval)
		}
		if err := applyAgentEnrollmentDefaults(*enrollmentFile, hubURL, agentID, role, tokenFile, caCert, clientCert, clientKey, ipMode, publicIPInterval); err != nil {
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
		return agentPollLoopWithTLSAndNetwork(*hubURL, *agentID, secret, *role, *stateDir, *conf, *interval, *maxTasks, time.Duration(*timeout)*time.Second, *topologyInterval, tlsConfig, *ipMode, *publicIPInterval)
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
		ipMode := fs.String("ip-mode", "static", "agent IP mode: static or dynamic")
		publicIPInterval := fs.Int("public-ip-interval", defaultPublicIPIntervalSeconds, "seconds between public IP probes in dynamic mode")
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
			StateDir:                *stateDir,
			HubURL:                  *hubURL,
			Code:                    *code,
			AgentID:                 *agentID,
			Role:                    *role,
			Name:                    *name,
			Labels:                  *labels,
			CACertPath:              *caCert,
			TLSServerName:           *tlsServerName,
			IPMode:                  *ipMode,
			PublicIPIntervalSeconds: *publicIPInterval,
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
			poller := newAgentPoller(cfg.HubURL, cfg.AgentID, strings.TrimSpace(string(tokenData)), cfg.Role, *stateDir, *conf, 0, 10*time.Second, 0)
			poller.tlsConfig = tlsConfig
			if err := configurePollerNetwork(poller, cfg.IPMode, cfg.PublicIPIntervalSeconds); err != nil {
				return err
			}
			pollRes, err := poller.pollOnce()
			if err != nil {
				return err
			}
			res["poll"] = pollRes
		}
		return printJSON(res)
	case "agent-set-ip-mode":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		stateDir := fs.String("state-dir", defaultStateDir, "state directory")
		mode := fs.String("mode", "", "agent IP mode: static or dynamic")
		ipMode := fs.String("ip-mode", "", "alias for --mode")
		publicIPInterval := fs.Int("public-ip-interval", defaultPublicIPIntervalSeconds, "seconds between public IP probes in dynamic mode")
		_ = fs.Parse(args[1:])
		selectedMode := firstNonEmpty(*mode, *ipMode)
		if selectedMode == "" && fs.NArg() > 0 {
			selectedMode = fs.Arg(0)
		}
		res, err := updateAgentIPMode(*stateDir, selectedMode, *publicIPInterval)
		if err != nil {
			return err
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
  render-landing-socks --name NAME --server HOST --listen-port PORT --config-output PATH --endpoint-output PATH
  validate-endpoint ENDPOINT_JSON
  render-outbound ENDPOINT_JSON
  import-endpoint [--state-dir DIR] ENDPOINT_JSON
  export-endpoint [--state-dir DIR] NAME
  agent-connection-info [--state-dir DIR] [--conf PATH] [--auth-user USER] [--json]
  public-entry-set --use shadowsocks|wireguard|reality --name NAME --host HOST --public-port PORT --local-port PORT
  public-entry-list [--state-dir DIR] [--json]
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
  hub-create-enroll-code [--state-dir DIR] [--hub-url https://HUB:8443 | --public-host HOST] --agent-id ID --role transit|landing [--port 8443] [--ttl 10m] [--text]
  hub-dispatch [--state-dir DIR] --text "/status all"
  hub-dispatch [--state-dir DIR] --text "/link transit-hk landing-hk [auth_user] [endpoint_name] [--mode direct|mesh]"
  hub-dispatch [--state-dir DIR] --text "/probe transit-hk landing-hk [endpoint_name] [--timeout 3]"
  hub-dispatch [--state-dir DIR] --text "/update <hub|all|transit|landing|agent_id> <version|latest> [--restart]"
  hub-dispatch [--state-dir DIR] --text "/decommission <agent_id> [--mode detach|purge-managed-proxy|uninstall] [--confirm agent_id]"
  hub-tasks [--state-dir DIR] [--json]
  hub-results [--state-dir DIR] [--batch-id ID] [--json]
  hub-export-client [--state-dir DIR] --transit-id ID [--auth-user USER] [--format remnawave|json]
  hub-export-landing [--state-dir DIR] --landing-id ID [--endpoint-name NAME]
  hub-sync-agent [--state-dir DIR] --agent-id ID
  hub-sync-all [--state-dir DIR]
  hub-alert-offline [--state-dir DIR] [--dry-run]
  hub-alerts [--state-dir DIR] [--json]
  hub-alert-callback [--state-dir DIR] --data rp:obs:TOKEN
  hub-daemon [--state-dir DIR] [--host 127.0.0.1] [--port 8080] [--tls-cert CRT --tls-key KEY --client-ca CA --require-client-cert]
  bot-daemon [--state-dir DIR]
  agent-enroll --invite INVITE [--ip-mode static|dynamic] [--poll-once]
  agent-set-ip-mode --mode static|dynamic [--public-ip-interval 600]
  agent-enroll --code CODE --hub-url https://HUB:8443 --agent-id ID --role transit|landing [--ca-cert CA]
  agent-enroll --bundle BUNDLE [--poll-once]
  agent-poll-once --hub-url URL --agent-id ID --role transit|landing --token-file FILE [--ca-cert CA --client-cert CRT --client-key KEY]
  agent-poll-loop --hub-url URL --agent-id ID --role transit|landing --token-file FILE [--ip-mode static|dynamic] [--ca-cert CA --client-cert CRT --client-key KEY]
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
