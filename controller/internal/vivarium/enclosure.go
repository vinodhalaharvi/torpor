package vivarium

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Enclosure is a running fleet.
type Enclosure struct {
	fleet  *Fleet
	clock  *Clock
	log    func(string, ...interface{})
	organs []*organism
	gws    map[string]*gatewayState
	mu     sync.RWMutex
}

// Clock is time as the vivarium sees it.
//
// Separate from wall time because the interesting states are slow. A device
// reporting daily takes four days to become Unreachable at a stale multiplier
// of three, and nobody debugs a control plane on that schedule. At speed 60 a
// day of fleet behaviour takes twenty-four minutes to watch.
type Clock struct {
	start time.Time
	speed float64
}

func (c *Clock) Now() time.Time {
	if c.speed <= 1 {
		return time.Now()
	}
	elapsed := time.Since(c.start)
	return c.start.Add(time.Duration(float64(elapsed) * c.speed))
}

// Scale converts a fleet-time duration into wall-clock time.
func (c *Clock) Scale(d time.Duration) time.Duration {
	if c.speed <= 1 {
		return d
	}
	return time.Duration(float64(d) / c.speed)
}

type gatewayState struct {
	cfg    Gateway
	rng    *rand.Rand
	down   bool
	nextAt time.Time
	until  time.Time
	mu     sync.RWMutex
}

func (g *gatewayState) isDown(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cfg.OutageEvery.Duration == 0 {
		return false
	}
	if g.down && now.After(g.until) {
		g.down = false
		g.nextAt = now.Add(g.cfg.OutageEvery.Duration)
	}
	if !g.down && !g.nextAt.IsZero() && now.After(g.nextAt) {
		g.down = true
		g.until = now.Add(g.cfg.OutageFor.Or(10 * time.Minute))
	}
	if g.nextAt.IsZero() {
		g.nextAt = now.Add(g.cfg.OutageEvery.Duration)
	}
	return g.down
}

type organism struct {
	dev    Device
	cohort *Cohort
	rng    *rand.Rand
	cli    mqtt.Client
	enc    *Enclosure

	mu sync.Mutex
	// runningHash is what it is ACTUALLY running. reportedHash is what it
	// says. Keeping them separate is the whole reason this can reproduce the
	// failure where a device converges perfectly and does nothing.
	runningHash  string
	reportedHash string
	battery      float64
	bootCount    int
	silentUntil  time.Time
	wifiUpUntil  time.Time
	nextWiFiAt   time.Time
	bricked      bool
	boot         bool
}

func New(f *Fleet, log func(string, ...interface{})) *Enclosure {
	if f.Speed <= 0 {
		f.Speed = 1
	}
	e := &Enclosure{
		fleet: f,
		clock: &Clock{start: time.Now(), speed: f.Speed},
		log:   log,
		gws:   map[string]*gatewayState{},
	}
	for _, g := range f.Gateways {
		e.gws[g.Name] = &gatewayState{cfg: g, rng: rngFor(f.Seed, g.Name)}
	}
	for _, d := range f.Devices {
		o := &organism{
			dev:          d,
			rng:          rngFor(f.Seed, d.Name),
			enc:          e,
			runningHash:  d.ConfigHash,
			reportedHash: d.ConfigHash,
			battery:      d.Battery,
		}
		if o.battery == 0 {
			o.battery = 100
		}
		for i := range f.Cohorts {
			if f.Cohorts[i].Name == d.Cohort {
				o.cohort = &f.Cohorts[i]
			}
		}
		e.organs = append(e.organs, o)
	}
	return e
}

// Run connects every device and lets them live until the context ends.
func (e *Enclosure) Run(ctx context.Context, broker string) error {
	for _, o := range e.organs {
		if err := o.connect(broker); err != nil {
			return fmt.Errorf("%s: %w", o.dev.Name, err)
		}
		go o.live(ctx)
	}
	<-ctx.Done()
	for _, o := range e.organs {
		if o.cli != nil && o.cli.IsConnected() {
			o.cli.Disconnect(100)
		}
	}
	return nil
}

func (o *organism) connect(broker string) error {
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID("vivarium-" + o.dev.Name).
		SetAutoReconnect(true).
		SetCleanSession(true)

	prefix := o.topicPrefix()
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		// Subscribe to our own command topics, exactly as ESPHome does.
		c.Subscribe(prefix+"/+/+/command", 0, o.onCommand)
		// Retained birth, so the mapper's liveness signal is real.
		c.Publish(prefix+"/status", 0, true, "online")

		// And announce. The vivarium should satisfy the contract it exists to
		// test against — otherwise `torpor verify` against a vivarium device
		// reports gaps that are the vivarium's rather than the contract's,
		// which is exactly what happened the first time it was run.
		c.Publish("torpor/announce/"+o.dev.Name, 0, true, fmt.Sprintf(
			`{"device":%q,"model":"vivarium","gateway":%q,"nodeID":%d,`+
				`"topicPrefix":%q,"configHash":%q,"firmwareVersion":"vivarium",`+
				`"buildTime":%q}`,
			o.dev.Name, o.dev.Gateway, o.dev.NodeID, prefix,
			o.runningHash, time.Now().UTC().Format(time.RFC3339)))
	})
	opts.SetWill(prefix+"/status", "offline", 0, true)

	o.cli = mqtt.NewClient(opts)
	tok := o.cli.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("connect timeout")
	}
	return tok.Error()
}

func (o *organism) topicPrefix() string {
	if o.dev.TopicPrefix != "" {
		return o.dev.TopicPrefix
	}
	return o.dev.Name
}

// live is one device's whole existence.
func (o *organism) live(ctx context.Context) {
	clk := o.enc.clock
	interval := o.dev.ReportEvery.Or(30 * time.Second)

	// Stagger the first report, so forty devices do not all speak in the same
	// millisecond and produce a thundering herd that exists nowhere in reality.
	jitter := time.Duration(o.rng.Float64() * float64(interval))
	select {
	case <-time.After(clk.Scale(jitter)):
	case <-ctx.Done():
		return
	}

	for {
		o.tick()

		wait := interval
		if j := o.dev.Behaviour.Jitter; j > 0 {
			wait = time.Duration(float64(interval) * (1 + (o.rng.Float64()*2-1)*j))
		}
		select {
		case <-time.After(clk.Scale(wait)):
		case <-ctx.Done():
			return
		}
	}
}

func (o *organism) tick() {
	now := o.enc.clock.Now()

	o.mu.Lock()
	if o.bricked {
		o.mu.Unlock()
		return
	}
	if now.Before(o.silentUntil) {
		o.mu.Unlock()
		return
	}

	// Battery ages, so a fleet ages. Without this every device sits at 100%
	// forever and the battery refusal never fires against anything.
	if d := o.dev.BatteryDrainPerDay; d > 0 {
		elapsed := now.Sub(o.enc.clock.start)
		o.battery = o.dev.Battery - d*(elapsed.Hours()/24)
		if o.battery < 0 {
			o.battery = 0
		}
	}
	batt, hash, boots := o.battery, o.reportedHash, o.bootCount
	o.mu.Unlock()

	// A device behind a downed gateway is silent, and every device behind that
	// gateway is silent in the same instant. Correlated by construction.
	if o.dev.Gateway != "" {
		if g, ok := o.enc.gws[o.dev.Gateway]; ok && g.isDown(now) {
			return
		}
	}

	p := o.topicPrefix()
	o.pub(p+"/sensor/temperature/state", fmt.Sprintf("%.2f", 20+o.rng.Float64()*10))
	o.pub(p+"/sensor/humidity/state", fmt.Sprintf("%.2f", 35+o.rng.Float64()*15))
	o.pub(p+"/sensor/battery_percent/state", fmt.Sprintf("%.0f", batt))
	o.pub(p+"/text_sensor/running_config_hash/state", hash)
	o.pub(p+"/sensor/boot_count/state", fmt.Sprintf("%d", boots))

	if o.dev.Gateway != "" {
		// Relayed devices appear inside the gateway's frame stream, not on
		// topics of their own — the same shape the real gateway firmware
		// publishes.
		o.pubAs(o.dev.Gateway+"/lora/rx", fmt.Sprintf(
			`{"type":2,"from":%d,"rssi":%.0f,"snr":%.1f,"temperature":%.1f,"humidity":%.0f}`,
			o.dev.NodeID, -60-o.rng.Float64()*40, 8+o.rng.Float64()*4,
			20+o.rng.Float64()*10, 35+o.rng.Float64()*15))
	}
}

// onCommand is where a device decides whether to obey.
func (o *organism) onCommand(_ mqtt.Client, m mqtt.Message) {
	topic, payload := m.Topic(), string(m.Payload())
	if !strings.Contains(topic, "firmware_url") {
		return
	}

	// Echo first, always — before deciding whether to act.
	//
	// The contract asks for the echo so a controller can tell "received" from
	// "acted on", and a device that echoes only on success makes those two
	// indistinguishable. Even a no-op gets echoed.
	o.pubAs(o.topicPrefix()+"/text/firmware_url/state", payload)

	bar := strings.Index(payload, "|")
	if bar < 0 {
		return
	}
	want := payload[:bar]

	o.mu.Lock()
	if o.bricked || want == o.runningHash {
		o.mu.Unlock()
		return
	}
	mode := o.dev.Behaviour.OnFirmware
	// A cohort's incompatibility is deterministic, not a coin flip. Bad
	// firmware does not fail 2% of a hardware revision.
	if o.cohort != nil {
		for _, h := range o.cohort.FirmwareFailsFor {
			if h == want {
				mode = o.cohort.FailureMode
			}
		}
	}
	if c := o.dev.Behaviour.FailChance; c > 0 && o.rng.Float64() > c {
		mode = FailNone
	}
	delay := o.dev.Behaviour.FirmwareDelay.Or(2 * time.Second)
	o.mu.Unlock()

	o.enc.log("  %-10s firmware %s → %s%s", o.dev.Name, o.runningHash, want, modeNote(mode))

	go func() {
		time.Sleep(o.enc.clock.Scale(delay))
		o.mu.Lock()
		defer o.mu.Unlock()

		switch mode {
		case FailIgnores:
			return

		case FailBricks:
			o.bricked = true
			if o.cli != nil && o.cli.IsConnected() {
				o.cli.Publish(o.topicPrefix()+"/status", 0, true, "offline")
				o.cli.Disconnect(50)
			}

		case FailReportsWithoutRunning:
			// The failure that actually happened here. Reports the new hash,
			// keeps running the old one. Every observable signal says success.
			o.reportedHash = want

		case FailWrongHash:
			o.runningHash = "corrupt"
			o.reportedHash = "corrupt"

		case FailBootLoop:
			o.runningHash, o.reportedHash = want, want
			o.bootCount += 5

		case FailIntermittent:
			o.runningHash, o.reportedHash = want, want
			o.silentUntil = o.enc.clock.Now().Add(30 * time.Minute)

		default:
			o.runningHash, o.reportedHash = want, want
			o.bootCount++
		}
	}()
}

func (o *organism) pub(topic, val string) {
	if l := o.dev.Behaviour.PacketLoss; l > 0 && o.rng.Float64() < l {
		return // dropped. Indistinguishable from a device that did not speak.
	}
	o.pubAs(topic, val)
}

func (o *organism) pubAs(topic, val string) {
	if o.cli != nil && o.cli.IsConnected() {
		o.cli.Publish(topic, 0, false, val)
	}
}

func modeNote(m FailureMode) string {
	switch m {
	case FailNone:
		return ""
	case FailReportsWithoutRunning:
		return "  \033[31m(will report it without running it)\033[0m"
	case FailBootLoop:
		return "  \033[31m(will boot loop)\033[0m"
	case FailBricks:
		return "  \033[31m(will brick)\033[0m"
	case FailIgnores:
		return "  \033[33m(will ignore)\033[0m"
	case FailIntermittent:
		return "  \033[33m(will go quiet)\033[0m"
	}
	return "  (" + string(m) + ")"
}

// Snapshot reports what every device is really doing, as opposed to what it
// says.
//
// The vivarium's unfair advantage: it knows ground truth. A control plane can
// only ever see what devices report, so the only way to check whether it drew
// the right conclusion is to compare against something that knows — and that
// is the difference between testing the controller and testing the devices.
type Snapshot struct {
	Device       string
	Running      string
	Reported     string
	Lying        bool
	Battery      float64
	Bricked      bool
	BootCount    int
	GatewayDown  bool
}

func (e *Enclosure) Snapshot() []Snapshot {
	now := e.clock.Now()
	var out []Snapshot
	for _, o := range e.organs {
		o.mu.Lock()
		s := Snapshot{
			Device: o.dev.Name, Running: o.runningHash, Reported: o.reportedHash,
			Lying: o.runningHash != o.reportedHash, Battery: o.battery,
			Bricked: o.bricked, BootCount: o.bootCount,
		}
		o.mu.Unlock()
		if g, ok := e.gws[o.dev.Gateway]; ok {
			s.GatewayDown = g.isDown(now)
		}
		out = append(out, s)
	}
	return out
}
