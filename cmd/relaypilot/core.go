package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	invalidateHubCachesForPath(path)
	return nil
}

func invalidateHubCachesForPath(path string) {
	clean := filepath.Clean(path)
	if filepath.Base(clean) == hubAgentsName {
		invalidateHubAgentCache(filepath.Dir(clean))
		return
	}
	dir := filepath.Dir(clean)
	if filepath.Base(dir) == hubTasksDirName {
		invalidateHubTaskCache(filepath.Dir(dir))
	}
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
func hubExportsDir(stateDir string) string      { return filepath.Join(stateDir, hubExportsDirName) }
func agentPolicyPath(stateDir string) string    { return filepath.Join(stateDir, agentPolicyName) }
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
