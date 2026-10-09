package meter

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
)

type batteryControlStateReader struct {
	sync.Mutex
	get      func() (string, error)
	sample   string
	observed time.Time
}

var _ api.BatteryControlStateReader = (*batteryControlStateReader)(nil)

func (r *batteryControlStateReader) BatteryControlState() (api.BatteryControlState, error) {
	r.Lock()
	defer r.Unlock()
	var state api.BatteryControlState
	text, err := r.get()
	if err != nil {
		return state, err
	}
	var raw struct {
		Mode           api.BatteryOperatingMode
		NativeMode     string
		Power, Soc     *float64
		NativeCharging *bool
		Sample         string
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return state, fmt.Errorf("battery control state: %w", err)
	}
	if raw.Power == nil || raw.Soc == nil || raw.NativeCharging == nil || raw.Sample == "" ||
		math.IsNaN(*raw.Power) || math.IsInf(*raw.Power, 0) || math.IsNaN(*raw.Soc) || math.IsInf(*raw.Soc, 0) || *raw.Soc < 0 || *raw.Soc > 100 {
		return state, fmt.Errorf("battery control state incomplete or invalid")
	}
	if raw.Mode != api.BatteryOperatingAuto && raw.Mode != api.BatteryOperatingManual {
		raw.Mode = api.BatteryOperatingUnknown
	}
	if raw.Sample != r.sample {
		r.sample, r.observed = raw.Sample, time.Now()
	}
	state.Mode, state.NativeMode = raw.Mode, raw.NativeMode
	state.Power, state.Soc = *raw.Power, *raw.Soc
	state.NativeCharging, state.ObservedAt = *raw.NativeCharging, r.observed
	return state, nil
}
