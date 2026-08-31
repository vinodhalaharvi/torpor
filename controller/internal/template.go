package internal

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// Substitution is {{ .name }} and nothing else.
//
// Not Go templates, not Jinja. A device list is the most operationally
// dangerous document in the cluster — get a gateway assignment wrong and two
// hundred devices talk to the wrong radio — and it should be readable by
// somebody who has never seen this project. Conditionals and loops in a
// template are how a device list becomes a program, and a program is harder to
// review than a table.
var varRef = regexp.MustCompile(`\{\{\s*\.([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// RenderedDevice is one instantiated Device, in the shape the controller will
// write.
type RenderedDevice struct {
	Name       string
	Labels     map[string]string
	Protocol   string
	ConfigData map[string]string
	Properties []RenderedProperty
}

type RenderedProperty struct {
	Name          string
	CollectCycle  int64
	ReportCycle   int64
	ReportToCloud bool
	Visitors      map[string]string
	Desired       string
	// Overridden records that this device's value differs from the template's,
	// so "why is this one different" is answerable from the generated object
	// rather than by diffing against the template by hand.
	Overridden bool
}

// RenderTemplate instantiates every instance, returning what could be rendered
// and what could not.
//
// Errors are per-instance and non-fatal. One typo in one site's variables
// should not prevent the other hundred and ninety-nine devices from existing —
// the alternative is a fleet held hostage by a missing string.
func RenderTemplate(spec *fleetv1.DeviceTemplateSpec) ([]RenderedDevice, []fleetv1.TemplateError) {
	var out []RenderedDevice
	var errs []fleetv1.TemplateError

	// Property names, for validating overrides. An override naming a property
	// that does not exist is silently ignored otherwise, and a calibration
	// offset that quietly does nothing is worse than one that fails loudly.
	known := map[string]bool{}
	for _, p := range spec.Properties {
		known[p.Name] = true
	}

	for _, inst := range spec.Instances {
		rd := RenderedDevice{
			Name:       inst.Name,
			Labels:     map[string]string{},
			ConfigData: map[string]string{},
		}

		for k, v := range spec.Labels {
			rd.Labels[k] = v
		}
		for k, v := range inst.Labels {
			rd.Labels[k] = v
		}

		var missing []string
		if spec.Protocol != nil {
			rd.Protocol = spec.Protocol.Name
			for k, v := range spec.Protocol.ConfigData {
				rendered, miss := substitute(v, inst.Vars)
				missing = append(missing, miss...)
				rd.ConfigData[k] = rendered
			}
		}

		for _, p := range spec.Properties {
			rp := RenderedProperty{
				Name:          p.Name,
				CollectCycle:  p.CollectCycle,
				ReportCycle:   p.ReportCycle,
				ReportToCloud: p.ReportToCloud,
				Visitors:      map[string]string{},
				Desired:       p.Desired,
			}
			for k, v := range p.Visitors {
				rendered, miss := substitute(v, inst.Vars)
				missing = append(missing, miss...)
				rp.Visitors[k] = rendered
			}
			if ov, ok := inst.Overrides[p.Name]; ok {
				rendered, miss := substitute(ov, inst.Vars)
				missing = append(missing, miss...)
				rp.Desired = rendered
				rp.Overridden = true
			}
			rd.Properties = append(rd.Properties, rp)
		}

		for name := range inst.Overrides {
			if !known[name] {
				errs = append(errs, fleetv1.TemplateError{
					Instance: inst.Name,
					Reason:   fleetv1.TemplateErrBadOverride,
					Detail: fmt.Sprintf("override for %q, which the template does not define", name),
				})
			}
		}

		if len(missing) > 0 {
			sort.Strings(missing)
			errs = append(errs, fleetv1.TemplateError{
				Instance: inst.Name,
				Reason:   fleetv1.TemplateErrMissingVar,
				Detail:   "unset: " + strings.Join(dedupe(missing), ", "),
			})
			// Rendered anyway. A device missing one variable is better
			// examined than absent, and the error says which.
		}
		out = append(out, rd)
	}
	return out, errs
}

// substitute replaces {{ .x }} and reports which variables were unset.
//
// An unset variable renders as the literal reference rather than as empty
// string, on purpose. An empty topic prefix silently subscribes a device to
// everything; a literal "{{ .site }}" in a topic fails visibly and immediately.
func substitute(in string, vars map[string]string) (string, []string) {
	var missing []string
	out := varRef.ReplaceAllStringFunc(in, func(m string) string {
		name := varRef.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		missing = append(missing, name)
		return m
	})
	return out, missing
}

// DiffRendered reports which fields of a live device disagree with what the
// template would produce.
//
// Used to populate status.drifted rather than to correct anything. Somebody
// fixing a device at 3am is the system working, and a template that silently
// reverts a manual fix during an incident is how a control plane loses the
// trust of the people operating it.
func DiffRendered(want RenderedDevice, liveConfig map[string]string,
	liveDesired map[string]string) []string {
	var diffs []string
	for k, v := range want.ConfigData {
		if got, ok := liveConfig[k]; ok && got != v {
			diffs = append(diffs, fmt.Sprintf("protocol.%s: %q != %q", k, got, v))
		}
	}
	for _, p := range want.Properties {
		if p.Desired == "" {
			continue
		}
		if got, ok := liveDesired[p.Name]; ok && got != p.Desired {
			diffs = append(diffs, fmt.Sprintf("%s: %q != %q", p.Name, got, p.Desired))
		}
	}
	sort.Strings(diffs)
	return diffs
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
