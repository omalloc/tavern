package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type CounterSmoother struct {
	lastValue float64
	smoothed  float64
	Alpha     float64
	isInit    bool
}

func (s *CounterSmoother) Update(currentTotal float64) float64 {
	if !s.isInit {
		s.lastValue = currentTotal
		s.isInit = true
		return 0
	}

	delta := currentTotal - s.lastValue
	if delta < 0 {
		delta = 0
	}

	s.smoothed = s.Alpha*delta + (1-s.Alpha)*s.smoothed
	s.lastValue = currentTotal

	return s.smoothed
}

type RequestsCodeTotal struct {
	Code  string  `json:"code"`
	Count float64 `json:"count"`
}

func CollectorRequestsCodeTotal() []*RequestsCodeTotal {
	totals := make([]*RequestsCodeTotal, 0)
	if mfs := Gather(); mfs != nil {

		for _, mf := range mfs {
			if mf.GetName() == "tr_tavern_requests_code_total" {
				for _, metric := range mf.GetMetric() {
					for _, label := range metric.Label {
						if label.GetName() == "code" {
							totals = append(totals, &RequestsCodeTotal{
								Code:  label.GetValue(),
								Count: metric.GetCounter().GetValue(),
							})
						}
					}
				}
			}
		}
	}
	return totals
}

func Gather() []*dto.MetricFamily {
	familys, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return nil
	}
	return familys
}
