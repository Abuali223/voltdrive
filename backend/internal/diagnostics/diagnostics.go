// Package diagnostics runs fast, rule-based health checks on a vehicle snapshot.
// It uses NO AI/LLM, so the always-on proactive watcher costs nothing — the AI
// (Gemini) is only invoked on the user's explicit "Diagnose" tap. When a CAN
// device supplies real fault codes (Health.Faults), they are surfaced here too.
package diagnostics

import (
	"fmt"
	"strings"

	"voltdrive/backend/internal/provider"
)

// Issue is a single rule-detected problem.
type Issue struct {
	Severity string // info | warning | critical
	Title    string
}

// Check applies the rule set to a snapshot and returns any issues found.
func Check(s provider.Snapshot) []Issue {
	var out []Issue
	e := s.Energy

	// Real diagnostic trouble codes from a CAN device take priority.
	if len(s.Health.Faults) > 0 {
		out = append(out, Issue{"critical", "Nosozlik kodi: " + strings.Join(s.Health.Faults, ", ")})
	}
	// Battery / charging.
	switch {
	case e.BatteryLevel > 0 && e.BatteryLevel < 5 && !e.Charging:
		out = append(out, Issue{"critical", fmt.Sprintf("Batareya juda past (%d%%)", e.BatteryLevel)})
	case e.BatteryLevel > 0 && e.BatteryLevel < 15 && !e.Charging:
		out = append(out, Issue{"warning", fmt.Sprintf("Batareya past (%d%%) — zaryadlang", e.BatteryLevel)})
	}
	if e.PluggedIn && !e.Charging {
		out = append(out, Issue{"warning", "Ulangan, lekin zaryadlanmayapti"})
	}
	// Climate left on while parked drains the battery.
	if s.Climate.On && !s.EngineOn {
		out = append(out, Issue{"warning", "Klimat yoqilgan qolgan — batareya sarflanyapti"})
	}
	// Tyre pressure (kPa); under ~1.8 bar is low.
	for _, p := range s.Health.TirePressures {
		if p > 0 && p < 180 {
			out = append(out, Issue{"warning", "Shina bosimi past — tekshiring"})
			break
		}
	}
	return out
}

// Worst returns the highest severity present ("" if there are no issues).
func Worst(issues []Issue) string {
	rank := map[string]int{"info": 1, "warning": 2, "critical": 3}
	worst := ""
	for _, i := range issues {
		if rank[i.Severity] > rank[worst] {
			worst = i.Severity
		}
	}
	return worst
}
