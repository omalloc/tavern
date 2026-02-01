package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
)

var (
	endpoint     = "http://localhost:8081/plugin/graph"
	tickInterval = time.Second * 1
	startAt      = time.Now()
	uptime       = time.Unix(1767060001, 0)
)

func init() {}

func main() {
	if err := ui.Init(); err != nil {
		log.Fatalf("failed to initialize termui: %v", err)
	}
	defer ui.Close()

	collected := atomic.Bool{}

	client := http.Client{
		Timeout: time.Second * 2,
	}

	graph, _ := http.NewRequest(http.MethodGet, endpoint, nil)

	termWidth, termHeight := ui.TerminalDimensions()

	grid := ui.NewGrid()
	grid.SetRect(0, 3, termWidth, termHeight)

	banner, bannerDraw := func() (*widgets.Paragraph, func()) {
		banner := widgets.NewParagraph()
		banner.SetRect(0, 0, termWidth, 3)
		banner.Title = "Tavern"
		banner.Border = true

		textDraw := func() {
			color := "fg:red"
			status := "Disconnected"
			if collected.Load() {
				color = "fg:green"
				status = "Connected"
			}

			banner.Text = fmt.Sprintf("%s | Sampling @ [%s](fg:blue) | [%s](%s) (%s) | Uptime %s",
				endpoint, tickInterval.String(), status, color, startAt.Format(time.RFC1123), humanize.Time(uptime))
		}
		textDraw()

		return banner, textDraw
	}()

	fetch := func() map[string]float64 {
		resp, err := client.Do(graph)
		if err != nil {
			collected.Store(false)
			return nil
		}
		collected.Store(true)
		var data map[string]float64
		json.NewDecoder(resp.Body).Decode(&data)
		return data
	}

	rater, raterDraw := func() (*widgets.Paragraph, func()) {
		rater := widgets.NewParagraph()
		rater.Title = "Requests"
		rater.SetRect(0, 3, 50, 6)
		rater.BorderStyle.Fg = ui.ColorWhite
		rater.TitleStyle.Fg = ui.ColorCyan

		draw := func() {
			data := fetch()

			rater.Text = fmt.Sprintf("\nRequests/sec: %d \nTotal: %d \n2xx : %d\n4xx : %d\n499 : %d\n5xx : %d",
				int(data["total"]), int(data["total"]), int(data["2xx"]), int(data["4xx"]), int(data["499"]), int(data["5xx"]))
		}

		draw()
		return rater, draw
	}()

	load := widgets.NewGauge()
	load.Title = "CPU Usage"
	load.Percent = 40
	load.SetRect(0, 3, 50, 6)
	load.BarColor = ui.ColorMagenta
	load.BorderStyle.Fg = ui.ColorWhite
	load.TitleStyle.Fg = ui.ColorCyan

	grid.Set(
		ui.NewRow(1.0/2,
			ui.NewCol(1.0/2, rater),
			ui.NewCol(1.0/2, load),
		),
	)
	ui.Render(banner, grid)

	uiEvents := ui.PollEvents()
	ticker := time.NewTicker(time.Second).C
	for {
		select {
		case e := <-uiEvents:
			switch e.ID {
			case "q", "<C-c>":
				return
			}

			switch e.Type {
			case ui.ResizeEvent:
				payload := e.Payload.(ui.Resize)
				termWidth = payload.Width
				termHeight = payload.Height

				banner.SetRect(0, 0, termWidth, 3)
				grid.SetRect(0, 3, termWidth, termHeight)
				ui.Clear()
				ui.Render(banner, grid)
			}

		case <-ticker:
			bannerDraw()
			raterDraw()

			ui.Render(banner, grid)
		}
	}
}
