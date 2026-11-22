// SPDX-License-Identifier: AGPL-3.0-only

// Package metrics samples system resource usage for the overview page. It
// reads /proc and /sys only, so it runs in the unprivileged web process, and
// keeps a fixed-size in-memory history.
package metrics

import (
	"context"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"

	"github.com/Kobayashi-classmate/Cairn/internal/platform"
)

// Host describes the machine; it changes rarely.
type Host struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	Arch          string `json:"arch"`
	CPUModel      string `json:"cpuModel"`
	CPUCores      int    `json:"cpuCores"`
	UptimeSeconds uint64 `json:"uptimeSeconds"`
}

// Disk is the usage of one mounted filesystem.
type Disk struct {
	Mount  string `json:"mount"`
	FSType string `json:"fstype"`
	Total  uint64 `json:"total"`
	Used   uint64 `json:"used"`
}

// Current is the latest full sample.
type Current struct {
	TS           int64   `json:"ts"`
	CPUPercent   float64 `json:"cpuPercent"`
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	Load15       float64 `json:"load15"`
	MemTotal     uint64  `json:"memTotal"`
	MemUsed      uint64  `json:"memUsed"`
	MemAvailable uint64  `json:"memAvailable"`
	SwapTotal    uint64  `json:"swapTotal"`
	SwapUsed     uint64  `json:"swapUsed"`
	NetRxBps     float64 `json:"netRxBps"`
	NetTxBps     float64 `json:"netTxBps"`
	Disks        []Disk  `json:"disks"`
}

// Point is one compact history entry.
type Point struct {
	TS             int64   `json:"ts"`
	CPUPercent     float64 `json:"cpuPercent"`
	MemUsedPercent float64 `json:"memUsedPercent"`
	NetRxBps       float64 `json:"netRxBps"`
	NetTxBps       float64 `json:"netTxBps"`
}

// Overview is the payload of GET /api/v1/system/overview.
type Overview struct {
	Host    Host    `json:"host"`
	Current Current `json:"current"`
	History []Point `json:"history"`
}

// Collector samples periodically and serves the latest overview.
type Collector struct {
	interval time.Duration
	capacity int
	log      *slog.Logger

	mu      sync.RWMutex
	host    Host
	current Current
	history []Point // ring buffer, oldest first after reorder in Overview
	next    int
	full    bool

	lastRx, lastTx uint64
	lastNetAt      time.Time
}

// NewCollector samples every interval and keeps capacity history points.
func NewCollector(interval time.Duration, capacity int, log *slog.Logger) *Collector {
	return &Collector{interval: interval, capacity: capacity, log: log, history: make([]Point, capacity)}
}

// Run samples until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	c.refreshHost(ctx)
	_, _ = cpu.PercentWithContext(ctx, 0, false) // prime the CPU delta
	c.sample(ctx)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	hostEvery := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.sample(ctx)
			if hostEvery++; hostEvery%12 == 0 {
				c.refreshHost(ctx)
			}
		}
	}
}

// Overview returns a copy of the current state with history oldest first.
func (c *Collector) Overview() Overview {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var hist []Point
	if c.full {
		hist = append(hist, c.history[c.next:]...)
	}
	hist = append(hist, c.history[:c.next]...)
	cur := c.current
	cur.Disks = append([]Disk(nil), c.current.Disks...)
	return Overview{Host: c.host, Current: cur, History: hist}
}

// Host returns the host information alone, without the usage history.
func (c *Collector) Host() Host {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.host
}

func (c *Collector) refreshHost(ctx context.Context) {
	h := Host{Arch: runtime.GOARCH}
	if info, err := host.InfoWithContext(ctx); err == nil {
		h.Hostname, h.Kernel, h.UptimeSeconds = info.Hostname, info.KernelVersion, info.Uptime
		if info.KernelArch != "" {
			h.Arch = info.KernelArch
		}
	}
	if osr, err := platform.ReadOSRelease(); err == nil {
		h.OS = osr.PrettyName
	}
	if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
		h.CPUModel = strings.TrimSpace(infos[0].ModelName)
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		h.CPUCores = n
	}
	c.mu.Lock()
	c.host = h
	c.mu.Unlock()
}

func (c *Collector) sample(ctx context.Context) {
	now := time.Now()
	cur := Current{TS: now.Unix()}
	if p, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(p) > 0 {
		cur.CPUPercent = round1(p[0])
	}
	if l, err := load.AvgWithContext(ctx); err == nil {
		cur.Load1, cur.Load5, cur.Load15 = l.Load1, l.Load5, l.Load15
	}
	var memPct float64
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		cur.MemTotal, cur.MemUsed, cur.MemAvailable = v.Total, v.Used, v.Available
		memPct = round1(v.UsedPercent)
	}
	if s, err := mem.SwapMemoryWithContext(ctx); err == nil {
		cur.SwapTotal, cur.SwapUsed = s.Total, s.Used
	}
	cur.NetRxBps, cur.NetTxBps = c.netRates(ctx, now)
	cur.Disks = c.disks(ctx)

	c.mu.Lock()
	c.current = cur
	c.history[c.next] = Point{TS: cur.TS, CPUPercent: cur.CPUPercent, MemUsedPercent: memPct, NetRxBps: cur.NetRxBps, NetTxBps: cur.NetTxBps}
	c.next = (c.next + 1) % c.capacity
	if c.next == 0 {
		c.full = true
	}
	c.mu.Unlock()
}

// virtualNIC matches interfaces whose traffic would be double counted.
func virtualNIC(name string) bool {
	for _, p := range []string{"lo", "veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "vxlan"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (c *Collector) netRates(ctx context.Context, now time.Time) (rx, tx float64) {
	stats, err := psnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return 0, 0
	}
	var sumRx, sumTx uint64
	for _, s := range stats {
		if !virtualNIC(s.Name) {
			sumRx += s.BytesRecv
			sumTx += s.BytesSent
		}
	}
	if !c.lastNetAt.IsZero() && sumRx >= c.lastRx && sumTx >= c.lastTx {
		if dt := now.Sub(c.lastNetAt).Seconds(); dt > 0 {
			rx, tx = round1(float64(sumRx-c.lastRx)/dt), round1(float64(sumTx-c.lastTx)/dt)
		}
	}
	c.lastRx, c.lastTx, c.lastNetAt = sumRx, sumTx, now
	return rx, tx
}

// disks lists physical filesystems once per device, preferring the shortest
// mount point (bind mounts of the same device are skipped).
func (c *Collector) disks(ctx context.Context) []Disk {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	sort.Slice(parts, func(i, j int) bool { return len(parts[i].Mountpoint) < len(parts[j].Mountpoint) })
	seen := map[string]bool{}
	var out []Disk
	for _, p := range parts {
		if seen[p.Device] {
			continue
		}
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, Disk{Mount: p.Mountpoint, FSType: p.Fstype, Total: u.Total, Used: u.Used})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
