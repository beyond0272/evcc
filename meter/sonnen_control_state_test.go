package meter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/stretchr/testify/require"
)

func TestSonnenActualControlState(t *testing.T) {
	for _, tc := range []struct {
		mode, system string
		want         api.BatteryOperatingMode
	}{
		{`"1"`, "OnGrid", api.BatteryOperatingManual},
		{`2`, "OnGrid", api.BatteryOperatingAuto},
		{`"10"`, "OnGrid", api.BatteryOperatingAuto},
		{`null`, "OnGrid", api.BatteryOperatingUnknown},
		{`"2"`, "OffGrid", api.BatteryOperatingUnknown},
	} {
		t.Run(tc.mode+tc.system, func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/api/v2/status", r.URL.Path)
				reads++
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"OperatingMode":%s,"SystemStatus":%q,"Pac_total_W":-123,"USOC":51,"BatteryCharging":true,"Timestamp":"2026-10-09 12:00:00"}`, tc.mode, tc.system)
			}))
			defer server.Close()
			m, err := NewFromConfig(t.Context(), "template", map[string]any{
				"template": "sonnenbatterie", "usage": "battery", "host": strings.TrimPrefix(server.URL, "http://"), "token": "test-only",
			})
			require.NoError(t, err)
			reader, ok := api.Cap[api.BatteryControlStateReader](m)
			require.True(t, ok)
			state, err := reader.BatteryControlState()
			require.NoError(t, err)
			require.Equal(t, tc.want, state.Mode)
			require.Equal(t, -123.0, state.Power)
			require.True(t, state.NativeCharging)
			require.Empty(t, state.LeaseID)
			require.False(t, api.HasCap[api.BatteryPVLeaseController](m))
			require.Equal(t, 1, reads)
		})
	}
}
