package vivarium

import (
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// Every one of these was wrong before the suffix check. fmt.Sscanf with a
// trailing literal reports success on the numeric part alone.
func TestDurationParsing(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"30s", 30 * time.Second},
		{"6h", 6 * time.Hour},
		{"90m", 90 * time.Minute},
		{"2d", 48 * time.Hour},
		{"1.5d", 36 * time.Hour},
		{"45s", 45 * time.Second},
	} {
		var d Duration
		if err := d.UnmarshalJSON([]byte(`"` + c.in + `"`)); err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if d.Duration != c.want {
			t.Errorf("%q parsed as %v, want %v", c.in, d.Duration, c.want)
		}
	}
}

func TestFleetParses(t *testing.T) {
	var f Fleet
	raw := []byte(`
speed: 120
devices:
  - name: field-07
    topicPrefix: field-07
    reportEvery: 5m
`)
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if got := f.Devices[0].ReportEvery.Duration; got != 5*time.Minute {
		t.Fatalf("reportEvery = %v, want 5m — devices would wait days to speak", got)
	}
	clk := &Clock{start: time.Now(), speed: f.Speed}
	if got := clk.Scale(5 * time.Minute); got != 2500*time.Millisecond {
		t.Errorf("Scale(5m) at 120x = %v, want 2.5s", got)
	}
}
