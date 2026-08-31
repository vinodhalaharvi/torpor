package internal

import (
	"testing"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func tmpl() *fleetv1.DeviceTemplateSpec {
	return &fleetv1.DeviceTemplateSpec{
		DeviceModelRef: "lora-sensor",
		Labels:         map[string]string{"role": "field-sensor"},
		Protocol: &fleetv1.TemplateProtocol{
			Name: "esphome-mqtt",
			ConfigData: map[string]string{
				"broker":  "tcp://192.168.68.113:1883",
				"gateway": "{{ .gateway }}",
				"nodeID":  "{{ .nodeID }}",
			},
		},
		Properties: []fleetv1.TemplateProperty{
			{Name: "temperature", CollectCycle: 10000, ReportToCloud: true,
				Visitors: map[string]string{"topic": "temperature"}},
			{Name: "sample_interval", Desired: "300"},
		},
		Instances: []fleetv1.TemplateInstance{
			{Name: "field-01", Vars: map[string]string{"gateway": "w10-a", "nodeID": "2"},
				Labels: map[string]string{"site": "north"}},
			{Name: "field-02", Vars: map[string]string{"gateway": "w10-a", "nodeID": "3"},
				Overrides: map[string]string{"sample_interval": "60"}},
			{Name: "field-03", Vars: map[string]string{"gateway": "w10-a"}},
			{Name: "field-04", Vars: map[string]string{"gateway": "w10-a", "nodeID": "5"},
				Overrides: map[string]string{"smaple_interval": "60"}},
		},
	}
}

func TestRenderTemplate(t *testing.T) {
	devs, errs := RenderTemplate(tmpl())
	for _, d := range devs {
		si := ""
		for _, p := range d.Properties {
			if p.Name == "sample_interval" {
				si = p.Desired
				if p.Overridden { si += " (override)" }
			}
		}
		t.Logf("  %-9s gateway=%-6s nodeID=%-14s labels=%v  sample_interval=%s",
			d.Name, d.ConfigData["gateway"], d.ConfigData["nodeID"], d.Labels, si)
	}
	for _, e := range errs {
		t.Logf("  ERROR %-9s %-24s %s", e.Instance, e.Reason, e.Detail)
	}

	if len(devs) != 4 { t.Fatalf("rendered %d, want 4 — one bad instance must not block the rest", len(devs)) }
	if devs[1].Properties[1].Desired != "60" || !devs[1].Properties[1].Overridden {
		t.Error("field-02 override not applied")
	}
	if devs[0].Properties[1].Desired != "300" {
		t.Error("field-01 should keep the fleet-wide value")
	}
	// An unset variable must render as the literal, not as empty.
	if devs[2].ConfigData["nodeID"] != "{{ .nodeID }}" {
		t.Errorf("unset var rendered as %q; empty would silently misconfigure", devs[2].ConfigData["nodeID"])
	}
	if len(errs) != 2 { t.Errorf("errors=%d want 2 (missing var, typo'd override)", len(errs)) }
}

func TestDiffRendered(t *testing.T) {
	devs, _ := RenderTemplate(tmpl())
	d := DiffRendered(devs[0],
		map[string]string{"gateway": "w10-b"},
		map[string]string{"sample_interval": "900"})
	for _, x := range d { t.Logf("  drift: %s", x) }
	if len(d) != 2 { t.Errorf("diffs=%d want 2", len(d)) }
}
