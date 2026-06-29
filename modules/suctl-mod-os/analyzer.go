// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
)

// ── Journal helper ────────────────────────────────────────────────────────────

func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), " ago"))
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid duration %q — use Go format e.g. 1h, 30m, 24h", s)
	}
	return time.Now().Add(-d), nil
}

func readJournal(service string, since time.Time, priority int, pattern string) ([]string, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	defer j.Close()
	if err := j.AddMatch("_SYSTEMD_UNIT=" + service + ".service"); err != nil {
		return nil, fmt.Errorf("filter journal: %w", err)
	}
	if !since.IsZero() {
		if err := j.SeekRealtimeUsec(uint64(since.UnixMicro())); err != nil {
			return nil, fmt.Errorf("seek journal: %w", err)
		}
	} else {
		j.SeekHead() //nolint:errcheck
	}
	var entries []string
	for {
		n, err := j.Next()
		if n == 0 || err != nil {
			break
		}
		entry, err := j.GetEntry()
		if err != nil {
			continue
		}
		if priority >= 0 {
			p, _ := strconv.Atoi(entry.Fields["PRIORITY"])
			if p > priority {
				continue
			}
		}
		msg := entry.Fields["MESSAGE"]
		if pattern != "" && !strings.Contains(msg, pattern) {
			continue
		}
		ts := time.UnixMicro(int64(entry.RealtimeTimestamp))
		entries = append(entries, ts.Format("Jan 02 15:04:05")+" "+msg)
	}
	return entries, nil
}

// ── Log capabilities ──────────────────────────────────────────────────────────

func cmdLogTail(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	service, _ := args["service"].(string)
	service = strings.TrimSpace(service)
	if service == "" {
		return failResult("INVALID_PARAMS", "service is required")
	}
	lines := 50
	if n, ok := args["lines"].(string); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && v > 0 {
			lines = v
		}
	}
	source, _ := args["source"].(string)
	if source != "" && source != "journald" {
		if _, err := os.Stat(source); os.IsNotExist(err) {
			return failResult("CALLABLE_FAILED", "log file not found: "+source)
		}
		out, err := exec.Command("tail", "-n", strconv.Itoa(lines), source).Output()
		if err != nil {
			return failResult("CALLABLE_FAILED", "tail: "+err.Error())
		}
		return okResult(map[string]interface{}{"service": service, "source": source, "output": string(out)})
	}
	j, err := sdjournal.NewJournal()
	if err != nil {
		return failResult("CALLABLE_FAILED", "open journal: "+err.Error())
	}
	defer j.Close()
	j.AddMatch("_SYSTEMD_UNIT=" + service + ".service") //nolint:errcheck
	j.SeekTail()                                         //nolint:errcheck
	var entries []string
	for len(entries) < lines {
		n, err := j.Previous()
		if n == 0 || err != nil {
			break
		}
		entry, err := j.GetEntry()
		if err != nil {
			continue
		}
		ts := time.UnixMicro(int64(entry.RealtimeTimestamp))
		entries = append(entries, ts.Format("Jan 02 15:04:05")+" "+entry.Fields["MESSAGE"])
	}
	for i, k := 0, len(entries)-1; i < k; i, k = i+1, k-1 {
		entries[i], entries[k] = entries[k], entries[i]
	}
	return okResult(map[string]interface{}{"service": service, "source": "journald", "output": strings.Join(entries, "\n")})
}

func cmdLogSince(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	service, _ := args["service"].(string)
	service = strings.TrimSpace(service)
	if service == "" {
		return failResult("INVALID_PARAMS", "service is required")
	}
	sinceStr, _ := args["since"].(string)
	if strings.TrimSpace(sinceStr) == "" {
		sinceStr = "1h"
	}
	since, err := parseSince(sinceStr)
	if err != nil {
		return failResult("INVALID_PARAMS", err.Error())
	}
	entries, err := readJournal(service, since, -1, "")
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	return okResult(map[string]interface{}{"service": service, "since": sinceStr, "output": strings.Join(entries, "\n")})
}

func cmdLogSearch(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	service, _ := args["service"].(string)
	service = strings.TrimSpace(service)
	if service == "" {
		return failResult("INVALID_PARAMS", "service is required")
	}
	pattern, _ := args["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return failResult("INVALID_PARAMS", "pattern is required")
	}
	source, _ := args["source"].(string)
	if source != "" && source != "journald" {
		out, _ := exec.Command("grep", "-n", "--", pattern, source).Output()
		return okResult(map[string]interface{}{"service": service, "pattern": pattern, "source": source, "output": string(out)})
	}
	entries, err := readJournal(service, time.Time{}, -1, pattern)
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	return okResult(map[string]interface{}{"service": service, "pattern": pattern, "output": strings.Join(entries, "\n")})
}

func cmdLogLevel(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	service, _ := args["service"].(string)
	service = strings.TrimSpace(service)
	if service == "" {
		return failResult("INVALID_PARAMS", "service is required")
	}
	level, _ := args["level"].(string)
	if strings.TrimSpace(level) == "" {
		level = "err"
	}
	priorityMap := map[string]int{"emerg": 0, "alert": 1, "crit": 2, "err": 3, "warning": 4, "notice": 5, "info": 6, "debug": 7}
	maxP, known := priorityMap[strings.ToLower(level)]
	if !known {
		return failResult("INVALID_PARAMS", "level must be one of: emerg, alert, crit, err, warning, notice, info, debug")
	}
	entries, err := readJournal(service, time.Time{}, maxP, "")
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	return okResult(map[string]interface{}{"service": service, "level": level, "output": strings.Join(entries, "\n")})
}

// ── Resource capabilities ─────────────────────────────────────────────────────

func parseProcStat() map[string][]uint64 {
	data, _ := os.ReadFile("/proc/stat")
	result := map[string][]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		vals := make([]uint64, 0, len(fields)-1)
		for _, f := range fields[1:] {
			v, _ := strconv.ParseUint(f, 10, 64)
			vals = append(vals, v)
		}
		result[fields[0]] = vals
	}
	return result
}

func cpuTotalIdle(vals []uint64) (total, idle uint64) {
	for i, v := range vals {
		total += v
		if i == 3 {
			idle = v
		}
	}
	return
}

func sortedCPUKeys(m map[string][]uint64) []string {
	keys := make([]string, 0, len(m))
	if _, ok := m["cpu"]; ok {
		keys = append(keys, "cpu")
	}
	for i := 0; ; i++ {
		name := fmt.Sprintf("cpu%d", i)
		if _, ok := m[name]; !ok {
			break
		}
		keys = append(keys, name)
	}
	return keys
}

func cmdResourceCPU(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	s1 := parseProcStat()
	time.Sleep(500 * time.Millisecond)
	s2 := parseProcStat()
	type coreUsage struct {
		Core     string  `json:"core"`
		UsagePct float64 `json:"usage_pct"`
	}
	var cores []coreUsage
	for _, name := range sortedCPUKeys(s1) {
		v1, v2 := s1[name], s2[name]
		if len(v1) < 4 || len(v2) < 4 {
			continue
		}
		total1, idle1 := cpuTotalIdle(v1)
		total2, idle2 := cpuTotalIdle(v2)
		dt := float64(total2 - total1)
		if dt == 0 {
			continue
		}
		usage := (1 - float64(idle2-idle1)/dt) * 100
		usage = float64(int(usage*10)) / 10
		cores = append(cores, coreUsage{Core: name, UsagePct: usage})
	}
	return okResult(map[string]interface{}{"cores": cores})
}

func cmdResourceMemory(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return failResult("CALLABLE_FAILED", "read /proc/meminfo: "+err.Error())
	}
	type memInfo struct {
		TotalKB     uint64 `json:"total_kb"`
		FreeKB      uint64 `json:"free_kb"`
		AvailableKB uint64 `json:"available_kb"`
		BuffersKB   uint64 `json:"buffers_kb"`
		CachedKB    uint64 `json:"cached_kb"`
		SwapTotalKB uint64 `json:"swap_total_kb"`
		SwapFreeKB  uint64 `json:"swap_free_kb"`
	}
	m := memInfo{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":     m.TotalKB = v
		case "MemFree:":      m.FreeKB = v
		case "MemAvailable:": m.AvailableKB = v
		case "Buffers:":      m.BuffersKB = v
		case "Cached:":       m.CachedKB = v
		case "SwapTotal:":    m.SwapTotalKB = v
		case "SwapFree:":     m.SwapFreeKB = v
		}
	}
	return okResult(m)
}

func cmdResourceDisk(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	out, err := exec.Command("df", "-h", "--output=source,size,used,avail,pcent,target").Output()
	if err != nil {
		return failResult("CALLABLE_FAILED", "df: "+err.Error())
	}
	return okResult(map[string]interface{}{"output": string(out)})
}

func cmdProcessTop(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	by, _ := args["by"].(string)
	if by == "" {
		by = "cpu"
	}
	n := 10
	if raw, ok := args["n"].(string); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
			n = v
		}
	}
	sortKey := "-%cpu"
	if by == "mem" {
		sortKey = "-%mem"
	}
	out, err := exec.Command("ps", "axo", "pid,user,pcpu,pmem,cmd", "--sort="+sortKey, "--no-headers").Output()
	if err != nil {
		return failResult("CALLABLE_FAILED", "ps: "+err.Error())
	}
	lines := strings.SplitN(string(out), "\n", n+2)
	if len(lines) > n {
		lines = lines[:n]
	}
	return okResult(map[string]interface{}{"sort": by, "n": n, "output": strings.Join(lines, "\n")})
}

func cmdNetworkPorts(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	out, err := exec.Command("ss", "-tlnp").Output()
	if err != nil {
		return failResult("CALLABLE_FAILED", "ss: "+err.Error())
	}
	return okResult(parseSS(string(out)))
}

func cmdNetworkConnections(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	out, err := exec.Command("ss", "-tanp").Output()
	if err != nil {
		return failResult("CALLABLE_FAILED", "ss: "+err.Error())
	}
	return okResult(parseSS(string(out)))
}

func parseSS(output string) []map[string]string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return nil
	}
	header := strings.Fields(lines[0])
	hasNetid := len(header) > 0 && header[0] == "Netid"
	var result []map[string]string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		row := make(map[string]string)
		if hasNetid {
			if len(parts) < 5 {
				continue
			}
			row["netid"] = parts[0]
			row["state"] = parts[1]
			row["recv_q"] = parts[2]
			row["send_q"] = parts[3]
			row["local"] = parts[4]
			if len(parts) > 5 {
				row["peer"] = parts[5]
			}
			if len(parts) > 6 {
				row["process"] = strings.Join(parts[6:], " ")
			}
		} else {
			if len(parts) < 4 {
				continue
			}
			row["state"] = parts[0]
			row["recv_q"] = parts[1]
			row["send_q"] = parts[2]
			row["local"] = parts[3]
			if len(parts) > 4 {
				row["peer"] = parts[4]
			}
			if len(parts) > 5 {
				row["process"] = strings.Join(parts[5:], " ")
			}
		}
		result = append(result, row)
	}
	return result
}
