package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

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
	_, err := runCommandOutput(timeout, name, args...)
	return err
}

func runCommandOutput(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func envBoolDefault(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "":
		return fallback
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func boolDefault(v any, fallback bool) bool {
	if v == nil {
		return fallback
	}
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
	return fallback
}

func findSingBoxBinary() string {
	if path := strings.TrimSpace(os.Getenv("RELAYPILOT_SINGBOX_BIN")); path != "" {
		return path
	}
	if info, err := os.Stat("/etc/sing-box/sing-box"); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return "/etc/sing-box/sing-box"
	}
	if path, err := exec.LookPath("sing-box"); err == nil {
		return path
	}
	return ""
}

func singBoxCheckConfig(conf string) (obj, error) {
	bin := findSingBoxBinary()
	if bin == "" {
		return obj{"status": "skipped", "reason": "sing-box-missing"}, nil
	}
	args := []string{"check", "-c", conf}
	if info, err := os.Stat(conf); err == nil && info.IsDir() {
		args = []string{"check", "-C", conf}
	}
	out, err := runCommandOutput(30*time.Second, bin, args...)
	if err != nil {
		return obj{"status": "failed", "binary": bin, "error": err.Error(), "output": lastNonEmptyLines(out, 6)}, fmt.Errorf("sing-box check failed: %w", err)
	}
	res := obj{"status": "ok", "binary": bin}
	if trimmed := lastNonEmptyLines(out, 4); trimmed != "" {
		res["output"] = trimmed
	}
	return res, nil
}

func serviceManagerCommand() (string, string) {
	if preferred := strings.TrimSpace(os.Getenv("RELAYPILOT_SERVICE_MANAGER")); preferred != "" {
		if path, err := exec.LookPath(preferred); err == nil {
			return preferred, path
		}
		return preferred, preferred
	}
	if path, err := exec.LookPath("rc-service"); err == nil {
		return "rc-service", path
	}
	if path, err := exec.LookPath("systemctl"); err == nil {
		return "systemctl", path
	}
	return "", ""
}

func serviceManagerRun(manager, path, service, action string) (obj, error) {
	var args []string
	switch manager {
	case "rc-service":
		args = []string{service, action}
	case "systemctl":
		if action == "status" {
			args = []string{"is-active", service}
		} else {
			args = []string{action, service}
		}
	default:
		return obj{"status": "skipped", "reason": "service-manager-missing"}, nil
	}
	out, err := runCommandOutput(30*time.Second, path, args...)
	if err != nil {
		return obj{"status": "failed", "manager": manager, "error": err.Error(), "output": lastNonEmptyLines(out, 6)}, err
	}
	res := obj{"status": "ok", "manager": manager}
	if trimmed := lastNonEmptyLines(out, 4); trimmed != "" {
		res["output"] = trimmed
	}
	return res, nil
}

func dataPlaneApplyMode(payload obj) string {
	mode := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		str(payload["apply_mode"]),
		os.Getenv("RELAYPILOT_SINGBOX_APPLY_MODE"),
		"reload-first",
	)))
	mode = strings.ReplaceAll(mode, "_", "-")
	switch mode {
	case "manual", "check", "check-only":
		return "manual"
	case "restart", "restart-only":
		return "restart"
	case "reload", "reload-only":
		return "reload"
	case "", "auto", "reload-first":
		return "reload-first"
	default:
		return "reload-first"
	}
}

func applyDataPlaneChange(conf string, payload obj) (obj, error) {
	mode := dataPlaneApplyMode(payload)
	apply := obj{"conf": conf, "mode": mode}
	if strings.TrimSpace(os.Getenv("RELAYPILOT_DISABLE_DATAPLANE_APPLY")) != "" {
		apply["applied"] = false
		apply["check"] = obj{"status": "skipped", "reason": "disabled"}
		apply["reload"] = obj{"status": "skipped", "reason": "disabled"}
		apply["restart"] = obj{"status": "skipped", "reason": "disabled"}
		apply["status"] = obj{"status": "skipped", "reason": "disabled"}
		return apply, nil
	}
	if !boolDefault(payload["apply_dataplane"], true) {
		apply["applied"] = false
		apply["check"] = obj{"status": "skipped", "reason": "apply-disabled"}
		apply["reload"] = obj{"status": "skipped", "reason": "apply-disabled"}
		apply["restart"] = obj{"status": "skipped", "reason": "apply-disabled"}
		apply["status"] = obj{"status": "skipped", "reason": "apply-disabled"}
		return apply, nil
	}
	check, err := singBoxCheckConfig(conf)
	apply["check"] = check
	if err != nil {
		apply["applied"] = false
		apply["reload"] = obj{"status": "skipped", "reason": "check-failed"}
		apply["restart"] = obj{"status": "skipped", "reason": "check-failed"}
		apply["status"] = obj{"status": "skipped", "reason": "check-failed"}
		return apply, err
	}
	if str(check["status"]) != "ok" {
		apply["applied"] = false
		apply["reload"] = obj{"status": "skipped", "reason": str(check["reason"])}
		apply["restart"] = obj{"status": "skipped", "reason": str(check["reason"])}
		apply["status"] = obj{"status": "skipped", "reason": str(check["reason"])}
		return apply, nil
	}
	restartEnabled := envBoolDefault("RELAYPILOT_AUTO_RESTART_SINGBOX", true)
	restartEnabled = boolDefault(payload["restart_service"], restartEnabled)
	service := firstNonEmpty(str(payload["service_name"]), envOrDefault("RELAYPILOT_SINGBOX_SERVICE_NAME", "sing-box"))
	apply["service"] = service
	if !restartEnabled {
		apply["applied"] = false
		apply["reload"] = obj{"status": "skipped", "reason": "restart-disabled"}
		apply["restart"] = obj{"status": "skipped", "reason": "restart-disabled"}
		apply["status"] = obj{"status": "skipped", "reason": "restart-disabled"}
		return apply, nil
	}
	if mode == "manual" {
		apply["applied"] = false
		apply["reload"] = obj{"status": "skipped", "reason": "manual"}
		apply["restart"] = obj{"status": "skipped", "reason": "manual"}
		apply["status"] = obj{"status": "skipped", "reason": "manual"}
		return apply, nil
	}
	manager, managerPath := serviceManagerCommand()
	if manager == "" {
		apply["applied"] = false
		apply["reload"] = obj{"status": "skipped", "reason": "service-manager-missing"}
		apply["restart"] = obj{"status": "skipped", "reason": "service-manager-missing"}
		apply["status"] = obj{"status": "skipped", "reason": "service-manager-missing"}
		return apply, nil
	}
	if mode == "reload" || mode == "reload-first" {
		reload, err := serviceManagerRun(manager, managerPath, service, "reload")
		apply["reload"] = reload
		if err == nil {
			apply["restart"] = obj{"status": "skipped", "reason": "reload-ok"}
			status, err := serviceManagerRun(manager, managerPath, service, "status")
			apply["status"] = status
			if err != nil {
				apply["applied"] = false
				return apply, fmt.Errorf("sing-box status check failed: %w", err)
			}
			apply["applied"] = true
			return apply, nil
		}
		if mode == "reload" {
			apply["applied"] = false
			apply["restart"] = obj{"status": "skipped", "reason": "reload-failed"}
			apply["status"] = obj{"status": "skipped", "reason": "reload-failed"}
			return apply, fmt.Errorf("sing-box reload failed: %w", err)
		}
	} else {
		apply["reload"] = obj{"status": "skipped", "reason": "restart-mode"}
	}
	restart, err := serviceManagerRun(manager, managerPath, service, "restart")
	apply["restart"] = restart
	if err != nil {
		apply["applied"] = false
		apply["status"] = obj{"status": "skipped", "reason": "restart-failed"}
		return apply, fmt.Errorf("sing-box restart failed: %w", err)
	}
	status, err := serviceManagerRun(manager, managerPath, service, "status")
	apply["status"] = status
	if err != nil {
		apply["applied"] = false
		return apply, fmt.Errorf("sing-box status check failed: %w", err)
	}
	apply["applied"] = true
	return apply, nil
}

func formatDataPlaneApplyText(apply obj) string {
	if len(apply) == 0 {
		return ""
	}
	if truthy(apply["applied"]) {
		reload := asObj(apply["reload"])
		restart := asObj(apply["restart"])
		if str(reload["status"]) == "ok" {
			return "sing-box 已热重载并确认运行"
		}
		if str(reload["status"]) == "failed" && str(restart["status"]) == "ok" {
			return "sing-box 热重载失败后已重启并确认运行"
		}
		return "sing-box 已重启并确认运行"
	}
	check := asObj(apply["check"])
	switch str(check["status"]) {
	case "failed":
		return "sing-box check 失败，配置未确认生效"
	case "skipped":
		reason := firstNonEmpty(str(check["reason"]), "skipped")
		return "配置已写入，但未自动校验/应用（" + reason + "）"
	}
	reload := asObj(apply["reload"])
	restart := asObj(apply["restart"])
	if str(reload["status"]) == "skipped" && str(restart["status"]) == "skipped" {
		reason := firstNonEmpty(str(reload["reason"]), str(restart["reason"]), "skipped")
		return "配置已写入并通过 check，但未自动应用（" + reason + "）"
	}
	if str(reload["status"]) == "failed" && str(restart["status"]) == "skipped" {
		return "配置已写入并通过 check，但 sing-box 热重载失败"
	}
	if str(reload["status"]) == "failed" && str(restart["status"]) == "failed" {
		return "配置已写入并通过 check，但 sing-box 热重载和重启均失败"
	}
	if str(restart["status"]) == "skipped" {
		reason := firstNonEmpty(str(restart["reason"]), "skipped")
		return "配置已写入并通过 check，但未自动重启（" + reason + "）"
	}
	if str(restart["status"]) == "failed" {
		return "配置已写入并通过 check，但 sing-box 重启失败"
	}
	status := asObj(apply["status"])
	if str(status["status"]) == "failed" {
		if str(reload["status"]) == "ok" {
			return "配置已写入并已请求热重载，但运行状态确认失败"
		}
		return "配置已写入并已请求重启，但运行状态确认失败"
	}
	return "配置已写入，数据面状态需人工确认"
}
