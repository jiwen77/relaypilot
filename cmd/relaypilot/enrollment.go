package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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
	StateDir                string
	HubURL                  string
	Code                    string
	AgentID                 string
	Role                    string
	Name                    string
	Labels                  string
	CACertPath              string
	TLSServerName           string
	IPMode                  string
	PublicIPIntervalSeconds int
}

type agentEnrollmentConfig struct {
	HubURL                  string `json:"hub_url"`
	AgentID                 string `json:"agent_id"`
	Role                    string `json:"role"`
	TokenFile               string `json:"token_file"`
	CACertPath              string `json:"ca_cert"`
	ClientCertPath          string `json:"client_cert"`
	ClientKeyPath           string `json:"client_key"`
	IPMode                  string `json:"ip_mode"`
	PublicIPIntervalSeconds int    `json:"public_ip_interval_seconds"`
	CreatedAt               int64  `json:"created_at"`
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

func upsertPendingHubAgent(stateDir string, registration obj, codeID string, expiresAt int64) error {
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return err
	}
	agentID := str(registration["id"])
	if agentID == "" {
		return errors.New("pending agent id is required")
	}
	agents := asObj(reg["agents"])
	existing := asObj(agents[agentID])
	if len(existing) > 0 && !isPendingAgent(existing) && int64Value(existing["last_seen"]) > 0 {
		return nil
	}
	pending := obj{}
	for k, v := range existing {
		pending[k] = v
	}
	for k, v := range registration {
		pending[k] = v
	}
	n := now()
	if int64Value(pending["created_at"]) == 0 {
		pending["created_at"] = firstPositiveInt64(int64Value(existing["created_at"]), n)
	}
	if int64Value(pending["registered_at"]) == 0 {
		pending["registered_at"] = firstPositiveInt64(int64Value(existing["registered_at"]), n)
	}
	pending["updated_at"] = n
	pending["enrollment_status"] = "pending"
	pending["enroll_code_id"] = codeID
	pending["enroll_expires_at"] = expiresAt
	health := asObj(pending["health"])
	health["status"] = "pending"
	pending["health"] = health
	delete(pending, "last_seen")
	agents[agentID] = pending
	reg["agents"] = agents
	if str(reg["kind"]) == "" {
		reg["kind"] = "relaypilot/hub-registry"
	}
	if int64Value(reg["version"]) == 0 {
		reg["version"] = version
	}
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
	opts = hydrateHubEnrollOptionsFromExisting(stateDir, opts)
	if opts.Role == "" {
		opts.Role = "transit"
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
	caPEM, err := os.ReadFile(hubTLSCACertPath(stateDir))
	if err != nil {
		return nil, err
	}
	if err := saveHubEnrollCodes(stateDir, store); err != nil {
		return nil, err
	}
	if err := upsertPendingHubAgent(stateDir, registration, codeID, expiresAt); err != nil {
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

func hydrateHubEnrollOptionsFromExisting(stateDir string, opts hubEnrollCodeOptions) hubEnrollCodeOptions {
	agentID := strings.TrimSpace(opts.AgentID)
	if agentID == "" {
		return opts
	}
	reg, err := loadHubRegistry(stateDir)
	if err != nil {
		return opts
	}
	existing := asObj(asObj(reg["agents"])[agentID])
	if len(existing) == 0 {
		return opts
	}
	if opts.Role == "" {
		opts.Role = str(existing["role"])
	}
	if opts.Name == "" {
		opts.Name = str(existing["name"])
	}
	if opts.Labels == "" {
		opts.Labels = formatLabels(asObj(existing["labels"]))
	}
	return opts
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
	return detectPublicIPWithTimeout(5 * time.Second)
}

func detectPublicIPWithTimeout(timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
	}
	client := &http.Client{Timeout: timeout}
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

type ipLocationResolver func(ip string, timeout time.Duration) (obj, error)

var hubIPLocationResolver ipLocationResolver = lookupIPAPILocation

func geoIPDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RELAYPILOT_GEOIP"))) {
	case "0", "false", "off", "disabled", "no":
		return true
	default:
		return false
	}
}

func isPublicRoutableIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	reserved := []string{
		"192.0.2.0/24",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"2001:db8::/32",
	}
	for _, cidr := range reserved {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return false
		}
	}
	return true
}

func networkLocationIP(network obj) string {
	if ip := strings.TrimSpace(str(network["public_ip"])); ip != "" {
		return ip
	}
	return strings.TrimSpace(str(network["observed_ip"]))
}

func maskIPForDisplay(ip string) string {
	ip = strings.TrimSpace(ip)
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.x.x", v4[0], v4[1])
	}
	v6 := parsed.To16()
	if v6 == nil {
		return ip
	}
	parts := make([]string, 0, 4)
	for i := 0; i < 8; i += 2 {
		parts = append(parts, fmt.Sprintf("%x", uint16(v6[i])<<8|uint16(v6[i+1])))
	}
	return strings.Join(parts, ":") + ":…"
}

func maybeEnrichNetworkLocation(network obj) {
	if len(network) == 0 || geoIPDisabled() {
		return
	}
	ipText := networkLocationIP(network)
	parsed := net.ParseIP(ipText)
	if !isPublicRoutableIP(parsed) {
		return
	}
	n := now()
	location := asObj(network["location"])
	if str(location["ip"]) == ipText && str(location["label"]) != "" && n-int64Value(location["checked_at"]) < geoIPLocationRefreshSeconds {
		return
	}
	if str(network["location_error_ip"]) == ipText && n-int64Value(network["location_error_at"]) < geoIPLocationRetrySeconds {
		return
	}
	resolver := hubIPLocationResolver
	if resolver == nil {
		return
	}
	resolved, err := resolver(ipText, defaultGeoIPLookupTimeout)
	if err != nil {
		network["location_error"] = err.Error()
		network["location_error_ip"] = ipText
		network["location_error_at"] = n
		return
	}
	resolved = normalizeLocation(resolved)
	if str(resolved["label"]) == "" {
		return
	}
	resolved["ip"] = ipText
	resolved["checked_at"] = n
	network["location"] = resolved
	delete(network, "location_error")
	delete(network, "location_error_ip")
	delete(network, "location_error_at")
}

func lookupIPAPILocation(ip string, timeout time.Duration) (obj, error) {
	if timeout <= 0 {
		timeout = defaultGeoIPLookupTimeout
	}
	tmpl := strings.TrimSpace(os.Getenv("RELAYPILOT_GEOIP_URL"))
	if tmpl == "" {
		tmpl = "https://ipapi.co/{ip}/json/"
	}
	escapedIP := url.PathEscape(ip)
	rawURL := ""
	if strings.Contains(tmpl, "{ip}") {
		rawURL = strings.ReplaceAll(tmpl, "{ip}", escapedIP)
	} else {
		rawURL = strings.TrimRight(tmpl, "/") + "/" + escapedIP + "/json/"
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "RelayPilot/"+buildVersion)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body, 8192)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("geoip lookup returned %d", resp.StatusCode)
	}
	var out obj
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if truthy(out["error"]) {
		msg := str(firstNonNil(out["reason"], out["message"]))
		if msg == "" {
			msg = "geoip lookup failed"
		}
		return nil, errors.New(msg)
	}
	return normalizeLocation(out), nil
}

func normalizeLocation(raw obj) obj {
	if raw == nil {
		return obj{}
	}
	countryCode := strings.ToUpper(strings.TrimSpace(str(firstNonNil(raw["country_code"], raw["country"]))))
	countryName := strings.TrimSpace(str(firstNonNil(raw["country_name"], raw["country"])))
	city := strings.TrimSpace(str(raw["city"]))
	label := locationLabel(countryCode, countryName, city)
	out := obj{
		"country_code": countryCode,
		"country":      countryDisplayName(countryCode, countryName),
		"city":         cityDisplayName(city),
		"label":        label,
	}
	if source := str(raw["source"]); source != "" {
		out["source"] = source
	} else {
		out["source"] = "ipapi.co"
	}
	return out
}

func locationLabel(countryCode, countryName, city string) string {
	country := countryDisplayName(countryCode, countryName)
	cityName := cityDisplayName(city)
	if country != "" && cityName != "" {
		return country + "·" + cityName
	}
	if country != "" {
		return country
	}
	return cityName
}

func countryDisplayName(code, name string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if zh := map[string]string{
		"US": "美国", "JP": "日本", "SG": "新加坡", "KR": "韩国", "HK": "中国香港", "TW": "中国台湾", "GB": "英国", "DE": "德国", "FR": "法国", "NL": "荷兰", "CA": "加拿大", "AU": "澳大利亚", "CH": "瑞士", "SE": "瑞典", "FI": "芬兰", "PL": "波兰", "RU": "俄罗斯", "BR": "巴西", "IN": "印度", "TH": "泰国", "VN": "越南", "MY": "马来西亚", "ID": "印度尼西亚", "PH": "菲律宾",
	}[code]; zh != "" {
		return zh
	}
	return strings.TrimSpace(name)
}

func cityDisplayName(city string) string {
	trimmed := strings.TrimSpace(city)
	if trimmed == "" {
		return ""
	}
	key := strings.ToLower(trimmed)
	if zh := map[string]string{
		"los angeles": "洛杉矶", "tokyo": "东京", "osaka": "大阪", "singapore": "新加坡", "seoul": "首尔", "hong kong": "香港", "taipei": "台北", "london": "伦敦", "frankfurt": "法兰克福", "berlin": "柏林", "paris": "巴黎", "amsterdam": "阿姆斯特丹", "new york": "纽约", "san jose": "圣何塞", "san francisco": "旧金山", "seattle": "西雅图", "chicago": "芝加哥", "dallas": "达拉斯", "ashburn": "阿什本", "toronto": "多伦多", "sydney": "悉尼", "melbourne": "墨尔本",
	}[key]; zh != "" {
		return zh
	}
	return trimmed
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
	return writeAgentEnrollmentFromPayload(decoded, token, opts)
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
	return writeAgentEnrollmentFromPayload(enrollment, token, opts)
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
	return writeAgentEnrollmentFromPayload(enrollment, token, opts)
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

func writeAgentEnrollmentFromPayload(decoded obj, token string, opts agentEnrollOptions) (obj, error) {
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	ipMode, err := normalizeIPMode(opts.IPMode)
	if err != nil {
		return nil, err
	}
	publicIPIntervalSeconds := opts.PublicIPIntervalSeconds
	if publicIPIntervalSeconds <= 0 {
		publicIPIntervalSeconds = defaultPublicIPIntervalSeconds
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
		HubURL:                  hubURL,
		AgentID:                 agentID,
		Role:                    role,
		TokenFile:               filepath.Join(stateDir, "agent-token"),
		CACertPath:              filepath.Join(stateDir, "hub-ca.crt"),
		ClientCertPath:          filepath.Join(stateDir, "agent.crt"),
		ClientKeyPath:           filepath.Join(stateDir, "agent.key"),
		IPMode:                  ipMode,
		PublicIPIntervalSeconds: publicIPIntervalSeconds,
		CreatedAt:               now(),
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
		"agent_id":                   paths.AgentID,
		"role":                       paths.Role,
		"hub_url":                    paths.HubURL,
		"enrollment_file":            agentEnrollmentPath(stateDir),
		"token_file":                 paths.TokenFile,
		"ca_cert":                    paths.CACertPath,
		"client_cert":                paths.ClientCertPath,
		"client_key":                 paths.ClientKeyPath,
		"ip_mode":                    paths.IPMode,
		"public_ip_interval_seconds": paths.PublicIPIntervalSeconds,
		"poll_once":                  fmt.Sprintf("relaypilot agent poll-once --enrollment-file %s", agentEnrollmentPath(stateDir)),
		"install_service":            fmt.Sprintf("relaypilot agent install-service --enrollment-file %s", agentEnrollmentPath(stateDir)),
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
		HubURL:                  str(data["hub_url"]),
		AgentID:                 str(data["agent_id"]),
		Role:                    str(data["role"]),
		TokenFile:               str(data["token_file"]),
		CACertPath:              str(data["ca_cert"]),
		ClientCertPath:          str(data["client_cert"]),
		ClientKeyPath:           str(data["client_key"]),
		IPMode:                  str(data["ip_mode"]),
		PublicIPIntervalSeconds: int(int64Value(data["public_ip_interval_seconds"])),
		CreatedAt:               int64Value(data["created_at"]),
	}, nil
}

func loadAgentEnrollmentFile(path string) (agentEnrollmentConfig, error) {
	data, err := loadJSON(path)
	if err != nil {
		return agentEnrollmentConfig{}, err
	}
	return agentEnrollmentConfig{
		HubURL:                  str(data["hub_url"]),
		AgentID:                 str(data["agent_id"]),
		Role:                    str(data["role"]),
		TokenFile:               str(data["token_file"]),
		CACertPath:              str(data["ca_cert"]),
		ClientCertPath:          str(data["client_cert"]),
		ClientKeyPath:           str(data["client_key"]),
		IPMode:                  str(data["ip_mode"]),
		PublicIPIntervalSeconds: int(int64Value(data["public_ip_interval_seconds"])),
		CreatedAt:               int64Value(data["created_at"]),
	}, nil
}

func applyAgentEnrollmentDefaults(path string, hubURL, agentID, role, tokenFile, caCert, clientCert, clientKey, ipMode *string, publicIPInterval *int) error {
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
	if ipMode != nil && *ipMode == "" {
		*ipMode = cfg.IPMode
	}
	if publicIPInterval != nil && *publicIPInterval <= 0 {
		*publicIPInterval = cfg.PublicIPIntervalSeconds
	}
	return nil
}

func updateAgentIPMode(stateDir, mode string, publicIPIntervalSeconds int) (obj, error) {
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	cfg, err := loadAgentEnrollment(stateDir)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeIPMode(mode)
	if err != nil {
		return nil, err
	}
	if publicIPIntervalSeconds <= 0 {
		publicIPIntervalSeconds = defaultPublicIPIntervalSeconds
	}
	cfg.IPMode = normalized
	cfg.PublicIPIntervalSeconds = publicIPIntervalSeconds
	if err := writeJSON(agentEnrollmentPath(stateDir), cfg, 0o600); err != nil {
		return nil, err
	}
	return obj{
		"agent_id":                   cfg.AgentID,
		"role":                       cfg.Role,
		"hub_url":                    cfg.HubURL,
		"enrollment_file":            agentEnrollmentPath(stateDir),
		"ip_mode":                    cfg.IPMode,
		"public_ip_interval_seconds": cfg.PublicIPIntervalSeconds,
	}, nil
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
