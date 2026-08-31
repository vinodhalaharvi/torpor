// torpor-plan answers "what would happen if I applied this" without a cluster.
//
// It reads manifests from disk, runs the same assessment code the controllers
// run, and prints the decisions. Nothing is written anywhere.
//
// The value is not convenience. Every object in this project exists to make a
// distinction other tools collapse — Refused from Failed, Blocked from
// Waiting, AtRisk from Expiring — and every one of those distinctions is a
// decision made before anything is transmitted. A tool that shows those
// decisions before you commit to them is the natural interface to a system
// built on refusing early.
//
// It deliberately shares code rather than reimplementing: DeriveCapabilityFrom,
// PlanRollout, AssessDrift, EvaluateQuery and EvaluateWindow are the same
// functions the reconcilers call. A dry run that exercises different code from
// the real thing is a dry run that lies.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
	"github.com/vinodhalaharvi/torpor/controller/internal"
)

type fleet struct {
	devices     []*unstructured.Unstructured
	reported    map[string]map[string]string // device -> property -> value
	lastSeen    map[string]time.Time
	rollouts    []fleetv1.FirmwareRollout
	windows     []fleetv1.MaintenanceWindow
	drifts      []fleetv1.FleetDrift
	queries     []fleetv1.FleetQuery
	expiries    []fleetv1.CredentialExpiry
	decoms      []fleetv1.DeviceDecommission
	templates   []fleetv1.DeviceTemplate
	certExpiry  map[string]time.Time
}

func main() {
	dir := flag.String("dir", "manifests", "directory of manifests to plan against")
	now := flag.String("now", "", "evaluate as if it were this time (RFC3339), for testing windows")
	flag.Parse()

	at := time.Now()
	if *now != "" {
		t, err := time.Parse(time.RFC3339, *now)
		if err != nil {
			die("bad --now: %v", err)
		}
		at = t
	}

	f, err := load(*dir)
	if err != nil {
		die("%v", err)
	}

	livenesses := f.buildLiveness(at)

	// Decommissioned devices are excluded from everything below. This is the
	// filter whose absence quietly corrupts every derived number.
	set := internal.NewDecommissionSet(f.decoms)
	live := set.Filter(livenesses)

	fmt.Printf("\n\033[1mtorpor plan\033[0m  %s  —  %d devices, %d excluded as decommissioned\n",
		at.Format("2006-01-02 15:04 MST"), len(live), len(livenesses)-len(live))

	f.reportTemplates()
	f.reportLiveness(live)
	f.reportWindows(at)
	f.reportRollouts(live, at)
	f.reportDrift(live, at)
	f.reportQueries(live, at)
	f.reportExpiry(live, at)
	fmt.Println()
}

// buildLiveness derives the liveness objects the controller would produce.
func (f *fleet) buildLiveness(now time.Time) []fleetv1.DeviceLiveness {
	var out []fleetv1.DeviceLiveness
	for _, d := range f.devices {
		name := d.GetName()
		cfg, _, _ := unstructured.NestedMap(d.Object, "spec", "protocol", "configData")

		cap := internal.DeriveCapabilityFrom(cfg, d.GetLabels())
		if props, ok := f.reported[name]; ok {
			cap.RunningConfigHash = props["running_config_hash"]
			cap.BatteryPercent = int32(atoiSafe(props["battery_percent"]))
			if v := props["reachable_via"]; v != "" {
				cap.ReachableVia = strings.Split(v, ",")
			}
		}

		l := fleetv1.DeviceLiveness{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.GetNamespace()},
			Status: fleetv1.DeviceLivenessStatus{
				Transport:  labelOr(d.GetLabels(), "transport", "wifi"),
				Capability: cap,
			},
		}
		if gw, ok := cfg["gateway"].(string); ok {
			l.Status.Gateway = gw
		}

		if seen, ok := f.lastSeen[name]; ok {
			t := metav1.NewTime(seen)
			l.Status.LastSeen = &t
			silent := now.Sub(seen)
			l.Status.SilentFor = short(silent)

			interval := 60 * time.Second
			if v, err := toI64(cfg["expectedIntervalSeconds"]); err == nil && v > 0 {
				interval = time.Duration(v) * time.Second
			}
			mult := int64(3)
			if v, err := toI64(cfg["staleMultiplier"]); err == nil && v > 0 {
				mult = v
			}
			switch {
			case silent <= interval:
				l.Status.State = fleetv1.StateOnline
				l.Status.Assessment = "ReportingOnSchedule"
			case silent <= interval*time.Duration(mult):
				l.Status.State = fleetv1.StateSleeping
				l.Status.Assessment = "WithinExpectedWakeInterval"
			default:
				l.Status.State = fleetv1.StateUnreachable
				l.Status.Assessment = "NoReportFor" + short(silent)
			}
		} else {
			l.Status.State = fleetv1.StateUnknown
			l.Status.Assessment = "NeverReported"
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f *fleet) reportTemplates() {
	if len(f.templates) == 0 {
		return
	}
	section("templates")
	for i := range f.templates {
		t := &f.templates[i]
		devs, errs := internal.RenderTemplate(&t.Spec)
		fmt.Printf("  %-24s %d instances\n", t.Name, len(devs))
		for _, e := range errs {
			fmt.Printf("    \033[33m! %-10s %-24s %s\033[0m\n", e.Instance, e.Reason, e.Detail)
		}
	}
}

func (f *fleet) reportLiveness(live []fleetv1.DeviceLiveness) {
	section("liveness")
	fmt.Printf("  %-12s %-10s %-12s %-9s %s\n", "DEVICE", "TRANSPORT", "STATE", "SILENT", "ASSESSMENT")
	for i := range live {
		l := &live[i]
		fmt.Printf("  %-12s %-10s %s%-12s\033[0m %-9s %s\n",
			l.Name, l.Status.Transport, stateColour(l.Status.State),
			l.Status.State, l.Status.SilentFor, l.Status.Assessment)
	}
}

func (f *fleet) reportWindows(now time.Time) {
	if len(f.windows) == 0 {
		return
	}
	section("maintenance windows")
	for i := range f.windows {
		w := &f.windows[i]
		st := internal.EvaluateWindow(&w.Spec, now)
		state := "\033[31mCLOSED\033[0m"
		if st.Open {
			state = "\033[32mOPEN\033[0m"
		}
		next := ""
		if st.NextOpen != nil {
			next = "opens " + st.NextOpen.Format("2006-01-02 15:04")
		}
		fmt.Printf("  %-24s %-16s %-34s %s\n", w.Name, state, st.Reason, next)
	}
}

func (f *fleet) reportRollouts(live []fleetv1.DeviceLiveness, now time.Time) {
	if len(f.rollouts) == 0 {
		return
	}
	section("rollouts")
	for i := range f.rollouts {
		ro := &f.rollouts[i]
		sel, _ := metav1.LabelSelectorAsSelector(ro.Spec.Selector)

		var scoped []fleetv1.DeviceLiveness
		var devLabels []labels.Set
		for j := range live {
			for _, d := range f.devices {
				if d.GetName() != live[j].Name {
					continue
				}
				l := labels.Set(d.GetLabels())
				if sel == nil || sel.Matches(l) {
					scoped = append(scoped, live[j])
					devLabels = append(devLabels, l)
				}
			}
		}

		plan := internal.PlanRollout(ro, scoped)

		// The window check, in the same order the controller applies it:
		// after planning, before dispatch.
		var wdec internal.WindowDecision
		wdec.Open = true
		for k := range f.windows {
			w := &f.windows[k]
			wsel, _ := metav1.LabelSelectorAsSelector(w.Spec.Selector)
			hit := w.Spec.Selector == nil
			for _, l := range devLabels {
				if wsel != nil && wsel.Matches(l) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			st := internal.EvaluateWindow(&w.Spec, now)
			if !st.Open {
				wdec = internal.WindowDecision{Open: false, Window: w.Name,
					Reason: st.Reason, NextOpen: st.NextOpen}
			}
		}

		phase := "\033[32mwould proceed\033[0m"
		if !wdec.Open {
			phase = fmt.Sprintf("\033[31mBLOCKED\033[0m by %s (%s)", wdec.Window, wdec.Reason)
		} else if len(plan.Eligible) == 0 {
			phase = "\033[33mWAITING\033[0m — nothing eligible"
		}

		fmt.Printf("  \033[1m%s\033[0m  target=%d eligible=%d refused=%d pending=%d  %s\n",
			ro.Name, len(scoped), len(plan.Eligible), len(plan.Refused), len(plan.Pending), phase)

		if len(plan.Eligible) > 0 {
			steps := ro.Spec.Strategy.Steps
			if len(steps) == 0 {
				steps = []int32{100}
			}
			var parts []string
			for _, pct := range steps {
				parts = append(parts, fmt.Sprintf("%d%%→%d", pct,
					internal.StepSize(len(plan.Eligible), pct)))
			}
			fmt.Printf("    steps over %d eligible: %s\n", len(plan.Eligible), strings.Join(parts, "  "))
			fmt.Printf("    canary would be: %s\n", plan.Eligible[0])
		}
		for _, o := range plan.Refused {
			fmt.Printf("    \033[31mREFUSED\033[0m %-11s %-28s %s\n", o.Device, o.Reason, o.Detail)
		}
		for _, o := range plan.Pending {
			fmt.Printf("    \033[33mPENDING\033[0m %-11s %-28s %s\n", o.Device, o.Reason, o.Detail)
		}
	}
}

func (f *fleet) reportDrift(live []fleetv1.DeviceLiveness, now time.Time) {
	if len(f.drifts) == 0 {
		return
	}
	section("drift")
	for i := range f.drifts {
		d := &f.drifts[i]
		st := internal.AssessDrift(&d.Spec, live, now)
		fmt.Printf("  \033[1m%s\033[0m  total=%d converged=%d drifted=%d unknown=%d oldest=%s\n",
			d.Name, st.Total, st.Converged, st.Drifted, st.Unknown, st.OldestDrift)
		for _, dd := range st.Devices {
			fmt.Printf("    %-11s %-8s → %-8s age=%-8s remediable=%-5v %s\n",
				dd.Device, dd.Expected, dd.Actual, dd.DriftAge, dd.Remediable, dd.Assessment)
		}
	}
}

func (f *fleet) reportQueries(live []fleetv1.DeviceLiveness, now time.Time) {
	if len(f.queries) == 0 {
		return
	}
	section("queries")
	fmt.Printf("  %-24s %-9s %-12s %s\n", "QUERY", "MATCHED", "ACTIONABLE", "DEVICES")
	for i := range f.queries {
		q := &f.queries[i]
		st := internal.EvaluateQuery(&q.Spec, live, now)
		var names []string
		for _, d := range st.Devices {
			names = append(names, d.Device)
		}
		fmt.Printf("  %-24s %-9d %-12d %s\n",
			q.Name, st.Matched, st.Summary["actionable"], strings.Join(names, " "))
	}
}

func (f *fleet) reportExpiry(live []fleetv1.DeviceLiveness, now time.Time) {
	if len(f.expiries) == 0 {
		return
	}
	section("credentials")
	for i := range f.expiries {
		e := &f.expiries[i]
		st := internal.AssessCredentials(&e.Spec, live, f.certExpiry, now)
		fmt.Printf("  \033[1m%s\033[0m  healthy=%d expiring=%d \033[31matRisk=%d\033[0m expired=%d\n",
			e.Name, st.Healthy, st.Expiring, st.AtRisk, st.Expired)
		for _, c := range st.Devices {
			if c.State == fleetv1.CredHealthy {
				continue
			}
			col := "\033[33m"
			if c.State == fleetv1.CredAtRisk || c.State == fleetv1.CredExpired {
				col = "\033[31m"
			}
			fmt.Printf("    %s%-9s\033[0m %-11s left=%-9s via=%-12v %s\n",
				col, c.State, c.Device, c.TimeLeft,
				c.RotatableVia, c.ActionRequired)
		}
	}
}

// --- loading ---------------------------------------------------------------

func load(dir string) (*fleet, error) {
	f := &fleet{
		reported:   map[string]map[string]string{},
		lastSeen:   map[string]time.Time{},
		certExpiry: map[string]time.Time{},
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no yaml in %s", dir)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			var probe struct {
				Kind string `json:"kind"`
			}
			if yaml.Unmarshal([]byte(doc), &probe) != nil || probe.Kind == "" {
				continue
			}
			if err := f.add(probe.Kind, []byte(doc)); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s in %s: %v\n", probe.Kind, filepath.Base(path), err)
			}
		}
	}
	return f, nil
}

func (f *fleet) add(kind string, doc []byte) error {
	switch kind {
	case "Device":
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, u); err != nil {
			return err
		}
		f.devices = append(f.devices, u)
	case "FirmwareRollout":
		var o fleetv1.FirmwareRollout
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		f.rollouts = append(f.rollouts, o)
	case "MaintenanceWindow":
		var o fleetv1.MaintenanceWindow
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		f.windows = append(f.windows, o)
	case "FleetDrift":
		var o fleetv1.FleetDrift
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		f.drifts = append(f.drifts, o)
	case "FleetQuery":
		var o fleetv1.FleetQuery
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		f.queries = append(f.queries, o)
	case "CredentialExpiry":
		var o fleetv1.CredentialExpiry
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		f.expiries = append(f.expiries, o)
	case "DeviceDecommission":
		var o fleetv1.DeviceDecommission
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		o.Status.Phase = fleetv1.DecomComplete
		f.decoms = append(f.decoms, o)
	case "DeviceTemplate":
		var o fleetv1.DeviceTemplate
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		f.templates = append(f.templates, o)
	case "ObservedState":
		// Not a real CRD. Stands in for what devices have reported, so a plan
		// can be run against a fleet that does not exist yet — which is the
		// whole point of planning before you buy the hardware.
		var o struct {
			Metadata struct{ Name string } `json:"metadata"`
			Spec     struct {
				LastSeen   string            `json:"lastSeen"`
				CertExpiry string            `json:"certExpiry"`
				Reported   map[string]string `json:"reported"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return err
		}
		n := o.Metadata.Name
		if t, err := time.Parse(time.RFC3339, o.Spec.LastSeen); err == nil {
			f.lastSeen[n] = t
		}
		if t, err := time.Parse(time.RFC3339, o.Spec.CertExpiry); err == nil {
			f.certExpiry[n] = t
		}
		f.reported[n] = o.Spec.Reported
	}
	return nil
}

// --- small helpers ---------------------------------------------------------

func section(name string) {
	fmt.Printf("\n\033[1m── %s %s\033[0m\n", name, strings.Repeat("─", 66-len(name)))
}

func stateColour(s fleetv1.LivenessState) string {
	switch s {
	case fleetv1.StateOnline:
		return "\033[32m"
	case fleetv1.StateSleeping:
		return "\033[36m"
	case fleetv1.StateUnreachable:
		return "\033[31m"
	}
	return "\033[33m"
}

func labelOr(m map[string]string, k, def string) string {
	if v, ok := m[k]; ok && v != "" {
		return v
	}
	return def
}

func atoiSafe(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func toI64(v interface{}) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case string:
		var x int64
		_, err := fmt.Sscanf(n, "%d", &x)
		return x, err
	}
	return 0, fmt.Errorf("not a number")
}

func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func die(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "torpor-plan: "+f+"\n", a...)
	os.Exit(1)
}
