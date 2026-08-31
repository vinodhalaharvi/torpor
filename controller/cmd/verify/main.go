// torpor verify checks whether a device satisfies the contract.
//
//	torpor verify --device field-01 --broker tcp://10.0.4.2:1883
//
// It generates nothing and knows nothing about firmware. It watches MQTT and
// reports which parts of docs/device-contract.md the device actually
// implements — which is why it serves somebody on Zephyr or a vendor stack as
// well as somebody on ESPHome. The contract is behavioural, so the check is
// behavioural.
//
// The output that matters is the last line. A device satisfying four of five
// items is not broken; it is monitorable and not updatable, which is a real
// and useful state. Telling somebody that plainly is better than a pass/fail
// that implies their device is worthless because it cannot take firmware.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type result struct {
	Name     string
	Required bool
	OK       bool
	Detail   string
	Hint     string
	// Unlocks names what this check being green makes possible, so a failure
	// costs something legible rather than being an abstract deduction.
	Unlocks string
}

type observer struct {
	mu       sync.Mutex
	seen     map[string]string
	seenAt   map[string]time.Time
	retained map[string]bool
}

func (o *observer) record(topic, payload string, retained bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen[topic] = payload
	o.seenAt[topic] = time.Now()
	if retained {
		o.retained[topic] = true
	}
}

func (o *observer) get(suffix string) (topic, payload string, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for t, p := range o.seen {
		if strings.HasSuffix(t, suffix) {
			return t, p, true
		}
	}
	return "", "", false
}

func (o *observer) sawSince(suffix string, since time.Time) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for t, at := range o.seenAt {
		if strings.HasSuffix(t, suffix) && at.After(since) {
			return o.seen[t], true
		}
	}
	return "", false
}

func main() {
	device := flag.String("device", "", "device name / topic prefix")
	broker := flag.String("broker", "tcp://127.0.0.1:1883", "mqtt broker")
	wait := flag.Duration("wait", 45*time.Second, "how long to watch")
	testOTA := flag.Bool("test-ota", true, "write a firmware_url and see if the device answers")
	flag.Parse()

	if *device == "" {
		die("--device is required")
	}

	obs := &observer{
		seen:     map[string]string{},
		seenAt:   map[string]time.Time{},
		retained: map[string]bool{},
	}

	opts := mqtt.NewClientOptions().
		AddBroker(*broker).
		SetClientID("torpor-verify").
		SetCleanSession(true)
	cli := mqtt.NewClient(opts)
	if tok := cli.Connect(); !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		die("cannot reach broker %s: %v", *broker, tok.Error())
	}
	defer cli.Disconnect(100)

	cli.Subscribe(*device+"/#", 0, func(_ mqtt.Client, m mqtt.Message) {
		obs.record(m.Topic(), string(m.Payload()), m.Retained())
	})
	cli.Subscribe("torpor/announce/"+*device, 0, func(_ mqtt.Client, m mqtt.Message) {
		obs.record(m.Topic(), string(m.Payload()), m.Retained())
	})
	// Relayed devices have no topics of their own; they appear inside a
	// gateway's frame stream. Watch every gateway, and match by sender id later.
	cli.Subscribe("+/lora/rx", 0, func(_ mqtt.Client, m mqtt.Message) {
		obs.record(m.Topic()+"#"+time.Now().Format("150405.000"), string(m.Payload()), false)
	})

	fmt.Printf("\n\033[1mtorpor verify\033[0m  %s on %s\n", *device, *broker)
	fmt.Printf("  watching for %s", *wait)

	// Retained messages arrive immediately; periodic ones need patience. The
	// wait is not padding — a device reporting every thirty seconds has said
	// nothing yet at second five, and calling that a failure would be wrong.
	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		fmt.Print(".")
	}
	fmt.Print("\n\n")

	var results []result
	results = append(results, checkStatus(obs, *device))
	results = append(results, checkSensors(obs, *device))
	results = append(results, checkRunningHash(obs, *device))
	results = append(results, checkAnnounce(obs, *device))
	if *testOTA {
		results = append(results, checkFirmware(cli, obs, *device))
	} else {
		results = append(results, result{
			Name: "accepts a firmware URL and pulls", Required: false,
			Detail: "skipped (--test-ota=false)", Unlocks: "FirmwareRollout"})
	}

	report(results)
}

// 1. Retained birth, and a will.
//
// The retained birth is what separates a device that has nothing to say from
// one that is gone. Without it, six hours of silence is ambiguous.
func checkStatus(o *observer, dev string) result {
	r := result{Name: "publishes a retained status", Required: false,
		Unlocks: "DeviceLiveness can tell gone from quiet"}

	topic, payload, ok := o.get("/status")
	if !ok {
		r.Detail = "nothing on " + dev + "/status"
		r.Hint = "publish \"online\" retained on connect, and set it as the MQTT will with \"offline\""
		return r
	}
	if !o.retained[topic] {
		r.Detail = fmt.Sprintf("%s = %q, but NOT retained", topic, payload)
		r.Hint = "set the retain flag — otherwise a controller that starts later sees nothing"
		return r
	}
	if !strings.EqualFold(strings.TrimSpace(payload), "online") {
		r.Detail = fmt.Sprintf("%s = %q; expected \"online\"", topic, payload)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("%s = online (retained)", topic)
	return r
}

// 2. Anything at all. Without a sensor value the device is a name.
func checkSensors(o *observer, dev string) result {
	r := result{Name: "publishes sensor values", Required: true,
		Unlocks: "the point"}

	o.mu.Lock()
	var found []string
	for t := range o.seen {
		if strings.Contains(t, "/sensor/") && strings.HasSuffix(t, "/state") {
			parts := strings.Split(t, "/")
			found = append(found, parts[len(parts)-2])
		}
	}
	o.mu.Unlock()

	if len(found) == 0 {
		r.Detail = "no <prefix>/sensor/<name>/state topics seen"
		r.Hint = "topics are per-device config, not code — any layout works, " +
			"but the Device manifest's visitors.topic must match it"
		return r
	}
	r.OK = true
	if len(found) > 4 {
		found = append(found[:4], fmt.Sprintf("+%d more", len(found)-4))
	}
	r.Detail = strings.Join(found, ", ")
	return r
}

// 3. The one that is not negotiable.
//
// It must describe the running firmware rather than echo an instruction. A
// device reporting the hash it was told proves nothing, and that failure
// happened on real hardware here — a tone that never played while the twin
// converged and every signal said success. This check cannot prove the value
// is honest; it can only prove one exists, which is why the hint says what it
// says.
func checkRunningHash(o *observer, dev string) result {
	r := result{Name: "reports what firmware it is RUNNING", Required: true,
		Unlocks: "FleetDrift, and the rollout health gate"}

	_, payload, ok := o.get("running_config_hash/state")
	if !ok {
		r.Detail = "no running_config_hash reported"
		r.Hint = "publish a build-time constant, or better, an image hash computed " +
			"from the running image (MCUboot's header). A value echoed back from " +
			"the last instruction proves nothing"
		return r
	}
	if strings.TrimSpace(payload) == "" {
		r.Detail = "running_config_hash is empty"
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("%q", payload)
	return r
}

// 4. Announcement. Optional, and it saves an afternoon of transcription.
func checkAnnounce(o *observer, dev string) result {
	r := result{Name: "announces itself", Required: false,
		Unlocks: "DeviceEnrollment — no hand-written manifests"}

	_, payload, ok := o.get("torpor/announce/" + dev)
	if !ok {
		r.Detail = "nothing retained on torpor/announce/" + dev
		r.Hint = "one retained publish on connect. Without it, somebody types a " +
			"Device manifest by hand, and transcription is where node ids get swapped"
		return r
	}
	var a struct {
		Device     string `json:"device"`
		Model      string `json:"model"`
		NodeID     int    `json:"nodeID"`
		Gateway    string `json:"gateway"`
		ConfigHash string `json:"configHash"`
		BuildTime  string `json:"buildTime"`
	}
	if err := json.Unmarshal([]byte(payload), &a); err != nil {
		r.Detail = "announcement is not valid JSON"
		return r
	}
	r.OK = true
	bits := []string{}
	if a.Model != "" {
		bits = append(bits, "model "+a.Model)
	}
	if a.Gateway != "" {
		bits = append(bits, fmt.Sprintf("node %d via %s", a.NodeID, a.Gateway))
	}
	if a.BuildTime != "" {
		bits = append(bits, "built "+a.BuildTime)
	} else {
		r.Hint = "consider adding buildTime — it catches a device flashed from a " +
			"stale cache before it joins and starts drifting"
	}
	r.Detail = strings.Join(bits, ", ")
	return r
}

// 5. The write path, tested by actually writing.
//
// Deliberately uses the hash the device already reports, so a conformant device
// correctly does NOTHING to its flash — it should recognise the no-op and
// refuse. What is being checked is whether it is subscribed and echoes state,
// not whether it will flash on demand. Verifying a device by making it flash
// would be a poor trade.
func checkFirmware(cli mqtt.Client, o *observer, dev string) result {
	r := result{Name: "accepts a firmware URL and pulls", Required: false,
		Unlocks: "FirmwareRollout — without it, monitoring only"}

	_, current, _ := o.get("running_config_hash/state")
	if current == "" {
		current = "verify-noop"
	}

	before := time.Now()
	topic := dev + "/text/firmware_url/command"
	payload := current + "|http://torpor.invalid/verify-probe.bin"
	cli.Publish(topic, 0, false, payload)

	// Give it time to echo. A device that pulls before echoing would fail this
	// unfairly, which is why the contract asks for the echo first.
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		if v, ok := o.sawSince("firmware_url/state", before); ok {
			r.OK = true
			r.Detail = fmt.Sprintf("echoed %q — subscribed and responding", v)
			if !strings.HasPrefix(v, current) {
				r.Hint = "echoed a different value than commanded; check the set action"
			}
			return r
		}
	}
	r.Detail = "wrote to " + topic + " and saw no state echo within 15s"
	r.Hint = "subscribe to <prefix>/text/firmware_url/command, publish the value " +
		"straight back on .../state, then pull. Deduplicate on the token — the " +
		"mapper writes on every collect cycle, not on change"
	return r
}

func report(rs []result) {
	green, total := 0, len(rs)
	missingRequired := false

	for _, r := range rs {
		mark, colour := "✘", "\033[31m"
		if r.OK {
			mark, colour, green = "✔", "\033[32m", green+1
		} else if !r.Required {
			colour = "\033[33m"
		}
		req := ""
		if r.Required && !r.OK {
			req = " \033[31m(required)\033[0m"
			missingRequired = true
		}
		fmt.Printf("  %s%s\033[0m %-38s %s%s\n", colour, mark, r.Name, r.Detail, req)
		if !r.OK && r.Hint != "" {
			fmt.Printf("      \033[90m%s\033[0m\n", wrap(r.Hint, 66, "      "))
		}
		if !r.OK && r.Unlocks != "" {
			fmt.Printf("      \033[90mwould unlock: %s\033[0m\n", r.Unlocks)
		}
	}

	fmt.Printf("\n  %d of %d.\n", green, total)

	// The line that matters. A partial implementation is a real and useful
	// state, and saying so plainly beats a pass/fail that implies a device is
	// worthless because it cannot take firmware.
	switch {
	case missingRequired:
		fmt.Printf("  \033[31mThis device cannot be managed.\033[0m A device that cannot report\n")
		fmt.Printf("  what it is running has nothing for a health gate to check.\n\n")
		os.Exit(1)
	case green == total:
		fmt.Printf("  \033[32mFully conformant.\033[0m Monitorable, updatable, and self-enrolling.\n\n")
	default:
		fmt.Printf("  \033[33mMonitorable, with gaps.\033[0m Everything unmarked above still works —\n")
		fmt.Printf("  the missing pieces degrade into Unknown and Refused, which are answers.\n\n")
	}
}

func wrap(s string, width int, indent string) string {
	var out, line string
	for _, w := range strings.Fields(s) {
		if len(line)+len(w)+1 > width {
			out += line + "\n" + indent
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	return out + line
}

func die(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "torpor verify: "+f+"\n", a...)
	os.Exit(1)
}
