package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
		network := asObj(payload["network"])
		if observedIP := observedIPFromRequest(r); observedIP != "" {
			network["observed_ip"] = observedIP
			network["observed_at"] = now()
		}
		agent, err := updateHeartbeat(s.stateDir, agentID, asObj(payload["topology"]), asObj(payload["health"]), network)
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

func observedIPFromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
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

func validateSOCKSCredentials(username, password string) error {
	if (username == "") != (password == "") {
		return errors.New("socks username and password must both be set or both be empty")
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
		if versionField := firstNonNil(out["version_field"], out["socks_version"]); versionField != nil {
			version := str(versionField)
			if version != "4" && version != "4a" && version != "5" {
				return nil, fmt.Errorf("unsupported socks version: %s", version)
			}
			out["socks_version"] = version
		}
		if err := validateSOCKSCredentials(str(out["username"]), str(out["password"])); err != nil {
			return nil, err
		}
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

func makeSOCKSEndpoint(name, server string, serverPort int, username, password, tag, description string) (obj, error) {
	if err := validateSOCKSCredentials(username, password); err != nil {
		return nil, err
	}
	if tag == "" {
		tag = "landing-" + name + "-socks"
	}
	endpoint := obj{"kind": endpointKind, "version": version, "name": name, "protocol": "socks", "server": server, "server_port": serverPort, "socks_version": "5", "tag": tag}
	if username != "" {
		endpoint["username"] = username
		endpoint["password"] = password
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

func makeLandingSOCKSConfig(listen string, listenPort int, username, password, tag string) (obj, error) {
	if err := validateSOCKSCredentials(username, password); err != nil {
		return nil, err
	}
	if _, err := ensureSafeTag(tag, "inbound.tag"); err != nil {
		return nil, err
	}
	port, err := ensurePort(listenPort, "listen_port")
	if err != nil {
		return nil, err
	}
	inbound := obj{"type": "socks", "tag": tag, "listen": listen, "listen_port": port}
	if username != "" {
		inbound["users"] = []any{obj{"username": username, "password": password}}
	}
	return obj{"log": obj{"level": "info", "timestamp": true}, "inbounds": []any{inbound}, "outbounds": []any{obj{"type": "direct", "tag": "direct"}}, "route": obj{"final": "direct"}}, nil
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

func renderLandingSOCKS(name, server, listen string, listenPort, serverPort int, username, password, inboundTag, endpointTag string) (obj, obj, error) {
	endpoint, err := makeSOCKSEndpoint(name, server, serverPort, username, password, endpointTag, name+" landing via sing-box SOCKS5")
	if err != nil {
		return nil, nil, err
	}
	config, err := makeLandingSOCKSConfig(listen, listenPort, username, password, inboundTag)
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

type publicEntryOptions struct {
	Use        string
	Name       string
	Host       string
	PublicPort int
	LocalPort  int
	Network    string
}

func publicEntriesPath(stateDir string) string {
	return filepath.Join(stateDir, publicEntriesName)
}

func normalizePublicEntryUse(use string) (string, error) {
	use = strings.ToLower(strings.TrimSpace(use))
	switch use {
	case "reality", "vless":
		return "reality", nil
	case "shadowsocks", "ss":
		return "shadowsocks", nil
	case "wireguard", "wg", "mesh":
		return "wireguard", nil
	default:
		return "", fmt.Errorf("public entry use must be reality, shadowsocks, or wireguard: %q", use)
	}
}

func publicEntryKey(use, name string) string {
	return use + ":" + name
}

func loadPublicEntryStore(stateDir string) (obj, error) {
	path := publicEntriesPath(stateDir)
	data, err := loadJSON(path)
	if errors.Is(err, os.ErrNotExist) {
		return obj{"kind": "relaypilot/public-entries", "version": version, "entries": obj{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if asObj(data["entries"]) == nil {
		data["entries"] = obj{}
	}
	return data, nil
}

func setPublicEntry(stateDir string, opts publicEntryOptions) (obj, error) {
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	use, err := normalizePublicEntryUse(opts.Use)
	if err != nil {
		return nil, err
	}
	name, err := ensureSafeName(firstNonEmpty(opts.Name, "default"), "public_entry.name")
	if err != nil {
		return nil, err
	}
	host, hostPort, err := normalizePublicEntryHost(opts.Host)
	if err != nil {
		return nil, err
	}
	if opts.PublicPort <= 0 && hostPort > 0 {
		opts.PublicPort = hostPort
	}
	publicPort, err := ensurePort(opts.PublicPort, "public_entry.public_port")
	if err != nil {
		return nil, err
	}
	localPort := 0
	if opts.LocalPort > 0 {
		localPort, err = ensurePort(opts.LocalPort, "public_entry.local_port")
		if err != nil {
			return nil, err
		}
	}
	network := strings.ToLower(strings.TrimSpace(opts.Network))
	if network == "" {
		if use == "wireguard" {
			network = "udp"
		} else {
			network = "tcp"
		}
	}
	if network != "tcp" && network != "udp" {
		return nil, errors.New("public entry network must be tcp or udp")
	}
	entry := obj{
		"use":         use,
		"name":        name,
		"host":        host,
		"public_port": publicPort,
		"network":     network,
		"updated_at":  now(),
	}
	if localPort > 0 {
		entry["local_port"] = localPort
	}
	store, err := loadPublicEntryStore(stateDir)
	if err != nil {
		return nil, err
	}
	entries := asObj(store["entries"])
	entries[publicEntryKey(use, name)] = entry
	store["entries"] = entries
	if err := writeJSON(publicEntriesPath(stateDir), store, 0o600); err != nil {
		return nil, err
	}
	return entry, nil
}

func normalizePublicEntryHost(raw string) (string, int, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", 0, errors.New("public entry host is required")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Hostname() == "" {
			return "", 0, fmt.Errorf("public entry host must be a DNS name, IP, or URL host, got %q", raw)
		}
		if u.Path != "" && u.Path != "/" {
			return "", 0, fmt.Errorf("public entry host must not include a path: %q", raw)
		}
		port, err := parseOptionalPort(u.Port())
		if err != nil {
			return "", 0, fmt.Errorf("invalid public entry port: %q", u.Port())
		}
		return u.Hostname(), port, nil
	}
	if strings.Contains(raw, "/") {
		return "", 0, fmt.Errorf("public entry host must be a DNS name or IP, got %q", raw)
	}
	if host, portString, err := net.SplitHostPort(raw); err == nil {
		port, err := parseOptionalPort(portString)
		if err != nil {
			return "", 0, fmt.Errorf("invalid public entry port: %q", portString)
		}
		return strings.Trim(host, "[]"), port, nil
	}
	return strings.Trim(raw, "[]"), 0, nil
}

func listPublicEntries(stateDir string) ([]obj, error) {
	store, err := loadPublicEntryStore(stateDir)
	if err != nil {
		return nil, err
	}
	entries := asObj(store["entries"])
	out := make([]obj, 0, len(entries))
	for _, raw := range entries {
		out = append(out, asObj(raw))
	}
	sort.Slice(out, func(i, j int) bool {
		left := publicEntryKey(str(out[i]["use"]), str(out[i]["name"]))
		right := publicEntryKey(str(out[j]["use"]), str(out[j]["name"]))
		return left < right
	})
	return out, nil
}

func formatPublicEntriesText(entries []obj) string {
	if len(entries) == 0 {
		return "(no public entries)"
	}
	var b strings.Builder
	for _, entry := range entries {
		local := ""
		if int64Value(entry["local_port"]) > 0 {
			local = fmt.Sprintf(" -> local:%d", int64Value(entry["local_port"]))
		}
		fmt.Fprintf(&b, "%s\t%s\t%s:%v\t%s%s\n", str(entry["use"]), str(entry["name"]), str(entry["host"]), entry["public_port"], str(entry["network"]), local)
	}
	return strings.TrimRight(b.String(), "\n")
}

func findPublicEntry(stateDir, use, name string) (obj, error) {
	normalizedUse, err := normalizePublicEntryUse(use)
	if err != nil {
		return nil, err
	}
	name = firstNonEmpty(strings.TrimSpace(name), "default")
	store, err := loadPublicEntryStore(stateDir)
	if err != nil {
		return nil, err
	}
	entries := asObj(store["entries"])
	for _, key := range []string{publicEntryKey(normalizedUse, name), publicEntryKey(normalizedUse, "default")} {
		entry := asObj(entries[key])
		if len(entry) > 0 {
			return entry, nil
		}
	}
	return nil, nil
}

func applyPublicEntryToEndpoint(stateDir string, endpoint obj) (obj, error) {
	if str(endpoint["protocol"]) != "shadowsocks" {
		return endpoint, nil
	}
	entry, err := findPublicEntry(stateDir, "shadowsocks", str(endpoint["name"]))
	if err != nil || len(entry) == 0 {
		return endpoint, err
	}
	out := obj{}
	for k, v := range endpoint {
		out[k] = v
	}
	out["local_server"] = endpoint["server"]
	out["local_server_port"] = endpoint["server_port"]
	out["server"] = entry["host"]
	out["server_port"] = entry["public_port"]
	out["public_entry"] = entry
	return validateEndpoint(out, true)
}

func exportEndpointPublicEntries(stateDir, name string) (obj, error) {
	out := obj{}
	wireguard, err := findPublicEntry(stateDir, "wireguard", name)
	if err != nil {
		return nil, err
	}
	if len(wireguard) > 0 {
		out["wireguard"] = wireguard
	}
	return out, nil
}

func publicWireGuardEndpointFromExportResult(result obj) (string, error) {
	entry := asObj(asObj(result["public_entries"])["wireguard"])
	if len(entry) == 0 {
		return "", nil
	}
	if str(entry["use"]) != "" && str(entry["use"]) != "wireguard" {
		return "", fmt.Errorf("wireguard public entry has invalid use: %q", entry["use"])
	}
	host := strings.TrimSpace(str(entry["host"]))
	if host == "" {
		return "", errors.New("wireguard public entry host is empty")
	}
	port, err := ensurePort(entry["public_port"], "wireguard public entry public_port")
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
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
	endpoint, err := loadEndpoint(path, true)
	if err != nil {
		return nil, err
	}
	return applyPublicEntryToEndpoint(stateDir, endpoint)
}

func endpointToOutbound(endpoint obj) (obj, error) {
	endpoint, err := validateEndpoint(endpoint, true)
	if err != nil {
		return nil, err
	}
	switch str(endpoint["protocol"]) {
	case "shadowsocks":
		out := obj{"type": "shadowsocks", "tag": endpoint["tag"], "server": endpoint["server"], "server_port": endpoint["server_port"], "method": endpoint["method"], "password": endpoint["password"]}
		switch network := str(endpoint["network"]); network {
		case "tcp", "udp":
			out["network"] = network
		case "", "tcp,udp", "udp,tcp":
			// sing-box Shadowsocks outbound accepts only a single network value;
			// omitting it preserves the default dual TCP/UDP behavior.
		default:
			return nil, fmt.Errorf("unsupported shadowsocks endpoint network: %s", network)
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
