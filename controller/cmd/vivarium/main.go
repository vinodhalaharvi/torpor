// vivarium keeps a fleet of devices under observation.
//
//	vivarium --fleet examples/vivarium/north-ridge.yaml
//
// Starts an embedded MQTT broker, connects a fleet of devices to it, and lets
// them live. They publish real MQTT on real topics; the mapper cannot tell
// them from boards.
//
// Self-contained on purpose. A vivarium that needs a broker set up first is a
// vivarium nobody runs, and the whole value is that it is one command away
// when the hardware is not.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"sigs.k8s.io/yaml"

	"github.com/vinodhalaharvi/torpor/controller/internal/vivarium"
)

func main() {
	fleetPath := flag.String("fleet", "examples/vivarium/north-ridge.yaml", "fleet description")
	addr := flag.String("listen", ":1883", "embedded broker address")
	external := flag.String("broker", "", "use an external broker instead of the embedded one")
	every := flag.Duration("truth", 20*time.Second, "how often to print ground truth")
	flag.Parse()

	raw, err := os.ReadFile(*fleetPath)
	if err != nil {
		die("%v", err)
	}
	var f vivarium.Fleet
	if err := yaml.Unmarshal(raw, &f); err != nil {
		die("%s: %v", *fleetPath, err)
	}

	broker := *external
	if broker == "" {
		srv := mqttserver.New(&mqttserver.Options{InlineClient: true})
		_ = srv.AddHook(new(auth.AllowHook), nil)
		if err := srv.AddListener(listeners.NewTCP(listeners.Config{
			ID: "vivarium", Address: *addr})); err != nil {
			die("listener: %v", err)
		}
		go func() {
			if err := srv.Serve(); err != nil {
				die("broker: %v", err)
			}
		}()
		time.Sleep(200 * time.Millisecond)
		broker = "tcp://127.0.0.1" + *addr
		fmt.Printf("\033[1mvivarium\033[0m  embedded broker on %s\n", broker)
	}

	speed := f.Speed
	if speed <= 0 {
		speed = 1
	}
	fmt.Printf("  %d devices, %d cohorts, %d gateways, seed=%d, speed=%.0fx\n",
		len(f.Devices), len(f.Cohorts), len(f.Gateways), f.Seed, speed)
	fmt.Printf("  point the mapper at %s\n\n", broker)

	enc := vivarium.New(&f, func(format string, a ...interface{}) {
		fmt.Printf(format+"\n", a...)
	})

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	go func() {
		t := time.NewTicker(*every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				printTruth(enc)
			case <-ctx.Done():
				return
			}
		}
	}()

	if err := enc.Run(ctx, broker); err != nil {
		die("%v", err)
	}
	fmt.Println("\nvivarium closed")
}

// printTruth shows what the devices are ACTUALLY doing.
//
// The vivarium's unfair advantage. A control plane can only see what devices
// report; the only way to check whether it drew the right conclusion is to
// compare against something that knows the truth. The `lying` column is the
// one that matters — a device reporting a hash it is not running is invisible
// from every other vantage point in the system.
func printTruth(enc *vivarium.Enclosure) {
	snap := enc.Snapshot()
	sort.Slice(snap, func(i, j int) bool { return snap[i].Device < snap[j].Device })

	fmt.Printf("\n\033[1m── ground truth %s\033[0m\n", time.Now().Format("15:04:05"))
	fmt.Printf("  %-11s %-9s %-9s %-6s %-6s %s\n",
		"DEVICE", "RUNNING", "REPORTED", "BATT", "BOOTS", "NOTE")
	for _, s := range snap {
		note := ""
		switch {
		case s.Bricked:
			note = "\033[31mBRICKED\033[0m"
		case s.Lying:
			note = "\033[31mREPORTS A HASH IT IS NOT RUNNING\033[0m"
		case s.GatewayDown:
			note = "\033[33mgateway down\033[0m"
		case s.BootCount > 3:
			note = "\033[33mboot looping\033[0m"
		}
		fmt.Printf("  %-11s %-9s %-9s %-6.0f %-6d %s\n",
			s.Device, s.Running, s.Reported, s.Battery, s.BootCount, note)
	}
	fmt.Println()
}

func die(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "vivarium: "+f+"\n", a...)
	os.Exit(1)
}
