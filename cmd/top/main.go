package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	terminal "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
	"github.com/samber/lo"
)

var (
	endpoint     = ""
	tickInterval = time.Second * 1
)

func init() {
	flag.StringVar(&endpoint, "endpoint", "http://localhost:8080/plugin/qs/graph", "The metrics endpoint to fetch data from tavern server.")
	flag.DurationVar(&tickInterval, "interval", time.Second*1, "The interval to fetch metrics.")
}

func main() {
	flag.Parse()

	newDashboard()
}

func newDashboard() {
	if err := terminal.Init(); err != nil {
		log.Fatalf("failed to initialize termui: %v", err)
	}
	defer terminal.Close()

	termWidth, _ := terminal.TerminalDimensions()

	collected := atomic.Bool{}
	cpuPercent := atomic.Uint32{}
	memUsage := atomic.Uint64{}
	memTotal := atomic.Uint64{}
	diskPercent := atomic.Uint64{} // mock
	diskUsage := atomic.Uint64{}
	diskTotal := atomic.Uint64{}
	startedAt := atomic.Int64{}

	// 高级监控指标 { 热点url 热点域名 热点磁盘 }
	list := widgets.NewList()
	list.Title = "Hot URLs"
	list.SetRect(0, 12, termWidth, 30)
	list.BorderStyle.Fg = terminal.ColorWhite
	list.TitleStyle.Fg = terminal.ColorCyan
	list.TextStyle.Fg = terminal.ColorYellow

	client := &http.Client{
		Transport: &http.Transport{},
	}

	var (
		dataMu        sync.RWMutex
		latestData    = make(map[string]float64)
		latestHotUrls []string
	)

	// Background SSE consumer
	go func() {
		for {
			func() {
				req, err := http.NewRequest(http.MethodGet, endpoint, nil)
				if err != nil {
					return
				}

				resp, err := client.Do(req)
				if err != nil {
					collected.Store(false)
					return
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					collected.Store(false)
					return
				}

				collected.Store(true)
				reader := bufio.NewReader(resp.Body)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "data:") {
						jsonStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
						if jsonStr == "" {
							continue
						}

						var rsp Graph
						if err := json.Unmarshal([]byte(jsonStr), &rsp); err == nil {
							dataMu.Lock()
							latestData = rsp.Data
							latestHotUrls = rsp.HotUrls
							dataMu.Unlock()

							startedAt.Store(rsp.StartedAt)
							cpuPercent.Store(uint32(rsp.Data["cpu_percent"]))
							memUsage.Store(uint64(rsp.Data["mem_usage"]))
							memTotal.Store(uint64(rsp.Data["mem_total"]))
							diskUsage.Store(uint64(rsp.Data["disk_usage"]))
							diskTotal.Store(uint64(rsp.Data["disk_total"]))
						}
					}
				}
			}()
			time.Sleep(time.Second) // Reconnect delay
		}
	}()

	// 基础监控指标 { qps, cpu, memory }
	metricGrid := terminal.NewGrid()
	metricGrid.SetRect(0, 3, termWidth, 20)

	banner, bannerDraw := func() (*widgets.Paragraph, func()) {
		banner := widgets.NewParagraph()
		banner.SetRect(0, 0, termWidth, 3)
		banner.Title = " Tavern    (PRESS q TO QUIT) "
		banner.Border = true

		textDraw := func() {
			color := "fg:red"
			status := "Disconnected"
			if collected.Load() {
				color = "fg:green"
				status = "Connected"
			}

			startAt := time.UnixMilli(startedAt.Load())

			banner.Text = fmt.Sprintf("%s | Sampling @ [%s](fg:blue) | [%s](%s) (%s) | Uptime %s",
				endpoint, tickInterval.String(), status, color, startAt.Format(time.RFC1123), humanize.Time(startAt))
		}
		textDraw()

		return banner, textDraw
	}()

	rater, raterDraw := func() (*widgets.Paragraph, func()) {
		rater := widgets.NewParagraph()
		rater.Title = "Requests"
		rater.SetRect(0, 3, 50, 6)
		rater.BorderStyle.Fg = terminal.ColorWhite
		rater.TitleStyle.Fg = terminal.ColorCyan

		draw := func() {
			dataMu.RLock()
			data := make(map[string]float64, len(latestData))
			for k, v := range latestData {
				data[k] = v
			}
			hotUrls := make([]string, len(latestHotUrls))
			copy(hotUrls, latestHotUrls)
			dataMu.RUnlock()

			rater.Text = fmt.Sprintf("\nRequests/sec: %d \nTotal: %d \n2xx : %d\n4xx : %d\n499 : %d\n5xx : %d",
				int(data["total"]), int(data["total"]), int(data["2xx"]), int(data["4xx"]), int(data["499"]), int(data["5xx"]))

			list.Rows = lo.Filter(lo.Map(hotUrls, toMap), filter)
		}

		draw()
		return rater, draw
	}()

	load, loadDraw := func() (*widgets.Gauge, func()) {
		load := widgets.NewGauge()
		load.Title = "CPU Usage"
		load.Percent = int(cpuPercent.Load())
		load.BarColor = terminal.ColorMagenta
		load.BorderStyle.Fg = terminal.ColorWhite
		load.TitleStyle.Fg = terminal.ColorCyan

		return load, func() {
			load.Percent = int(cpuPercent.Load())
		}
	}()

	mem, memDraw := func() (*widgets.Gauge, func()) {
		mem := widgets.NewGauge()
		mem.Title = "Memory Usage"
		usagePercent := 0
		if memTotal.Load() > 0 {
			usagePercent = int(float64(memUsage.Load()) / float64(memTotal.Load()) * 100)
		}
		mem.Percent = usagePercent
		mem.BarColor = terminal.ColorGreen
		mem.BorderStyle.Fg = terminal.ColorWhite
		mem.TitleStyle.Fg = terminal.ColorCyan

		return mem, func() {
			usagePercent := 0
			if memTotal.Load() > 0 {
				usagePercent = int(float64(memUsage.Load()) / float64(memTotal.Load()) * 100)
			}
			mem.Percent = usagePercent
			mem.Label = fmt.Sprintf("%d%% | Mem: %s / %s",
				usagePercent,
				humanize.Bytes(memUsage.Load()),
				humanize.Bytes(memTotal.Load()),
			)
		}
	}()

	disk, diskDraw := func() (*widgets.Gauge, func()) {
		disk := widgets.NewGauge()
		disk.Title = "Disk Usage"
		disk.Percent = int(diskPercent.Load())
		disk.BarColor = terminal.ColorYellow
		disk.BorderStyle.Fg = terminal.ColorWhite
		disk.TitleStyle.Fg = terminal.ColorCyan

		return disk, func() {
			disk.Percent = int(diskPercent.Load())
			disk.Label = fmt.Sprintf("%d%% | Disk: %s / %s",
				0,
				humanize.Bytes(diskUsage.Load()),
				humanize.Bytes(diskTotal.Load()),
			)
		}
	}()

	metricGrid.Set(
		terminal.NewRow(1.0/2,
			terminal.NewCol(1.0/2, rater),
			terminal.NewCol(1.0/2,
				terminal.NewRow(1.0/3, load),
				terminal.NewRow(1.0/3, mem),
				terminal.NewRow(1.0/3, disk),
			),
		),
	)

	terminal.Render(banner, metricGrid, list)

	uiEvents := terminal.PollEvents()
	ticker := time.NewTicker(time.Second).C
	for {
		select {
		case e := <-uiEvents:
			switch e.ID {
			case "q", "<C-c>":
				return
			}

			switch e.Type {
			case terminal.ResizeEvent:
				payload := e.Payload.(terminal.Resize)
				termWidth = payload.Width
				// termHeight = payload.Height

				banner.SetRect(0, 0, termWidth, 3)
				metricGrid.SetRect(0, 3, termWidth, 20)
				list.SetRect(0, 12, termWidth, 30)

				terminal.Clear()
				terminal.Render(banner, metricGrid, list)
			}

		case <-ticker:
			bannerDraw()
			raterDraw()
			memDraw()
			diskDraw()
			loadDraw()

			terminal.Render(banner, metricGrid, list)
		}
	}
}

func filter(s string, _ int) bool {
	if s == "" {
		return false
	}
	return true
}

func toMap(s string, i int) string {
	parts := strings.Split(s, "@@")
	if len(parts) != 3 {
		return ""
	}
	return fmt.Sprintf("[%02d] LastAccess=%s %s ReqCount=%s", i, parts[1], parts[0], parts[2])
}

type Graph struct {
	Data      map[string]float64 `json:"data"`
	HotUrls   []string           `json:"hot_urls"`
	StartedAt int64              `json:"started_at"`
}
