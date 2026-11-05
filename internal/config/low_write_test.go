package config

import "testing"

func TestLowWriteFlagsAreOptIn(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "true"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("XUI_DISABLE_METRICS_PERSISTENCE", value)
			t.Setenv("XUI_DISABLE_TRAFFIC_STATS", value)
			want := value == "true"
			if IsMetricsPersistenceDisabled() != want || IsTrafficStatsDisabled() != want {
				t.Fatalf("flags must be enabled only by literal true, got %q", value)
			}
		})
	}
}
