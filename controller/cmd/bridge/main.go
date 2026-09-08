// torpor-bridge makes a serial-attached device a fleet device.
//
//	torpor-bridge --device nrf-01 --port /dev/tty.usbmodem... --broker tcp://...
//
// It reads lines from a serial port and republishes them on MQTT, and
// subscribes to that device's command topics and writes them back down. The
// mapper cannot tell the result from a device that speaks MQTT itself.
//
// This is a gateway in exactly the sense w10-a is a gateway for field-01: a
// device with no address of its own, surfaced through something that has one.
// The pattern is already in the model, and nothing in the mapper changes.
//
// It is also the shape that covers a whole class of hardware this project
// could not previously touch — Modbus RTU, RS-485 sensors, older industrial
// gear, anything on a wire rather than a network. The nRF9160 happens to be
// the first one, because it is cellular with no SIM.
//
// Two things the bridge owns rather than the device, because only the bridge
// can know them:
//
//   the retained birth and the will — the device cannot announce its own
//   death, and a serial cable falling out is exactly the case that matters
//
//   retention — a device that boots before anyone is listening has still
//   announced itself, and a controller starting later must still see it
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"go.bug.st/serial"
)

type bridge struct {
	device string
	port   serial.Port
	cli    mqtt.Client
	mu     sync.Mutex

	// Counters, printed on exit. A bridge that has been running for a week
	// should be able to say whether it did anything.
	up, down, dropped int
}

func main() {
	device := flag.String("device", "", "device name, used as the MQTT topic prefix")
	portName := flag.String("port", "", "serial port")
	baud := flag.Int("baud", 115200, "baud rate")
	broker := flag.String("broker", "tcp://127.0.0.1:1883", "mqtt broker")
	verbose := flag.Bool("v", false, "print every line both ways")
	flag.Parse()

	if *device == "" || *portName == "" {
		die("--device and --port are required")
	}

	port, err := serial.Open(*portName, &serial.Mode{BaudRate: *baud})
	if err != nil {
		die("open %s: %v", *portName, err)
	}
	defer port.Close()
	// Without a read timeout a disconnected cable blocks forever and the
	// bridge looks alive while doing nothing.
	port.SetReadTimeout(2 * time.Second)

	b := &bridge{device: *device, port: port}

	opts := mqtt.NewClientOptions().
		AddBroker(*broker).
		SetClientID("torpor-bridge-" + *device).
		SetAutoReconnect(true).
		SetCleanSession(true)

	// The will belongs to the bridge, not the device. A device on a serial
	// cable cannot announce its own death — and the cable falling out is
	// precisely the failure this needs to report. If the bridge dies, the
	// device is unreachable, and saying so is correct.
	opts.SetWill(*device+"/status", "offline", 0, true)

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		filter := *device + "/+/+/command"
		c.Subscribe(filter, 0, b.onCommand)
		// ESPHome's two-segment command topics too.
		c.Subscribe(*device+"/+/command", 0, b.onCommand)
		fmt.Printf("  subscribed to %s\n", filter)
	})

	b.cli = mqtt.NewClient(opts)
	if tok := b.cli.Connect(); !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		die("broker %s: %v", *broker, tok.Error())
	}

	fmt.Printf("\n\033[1mtorpor-bridge\033[0m  %s  %s <-> %s\n\n",
		*device, *portName, *broker)

	ctx := make(chan os.Signal, 1)
	signal.Notify(ctx, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx
		fmt.Printf("\n  %d up, %d down, %d dropped\n", b.up, b.down, b.dropped)
		b.cli.Publish(*device+"/status", 0, true, "offline").WaitTimeout(2 * time.Second)
		b.cli.Disconnect(200)
		port.Close()
		os.Exit(0)
	}()

	b.readLoop(*verbose)
}

// readLoop is device -> MQTT.
func (b *bridge) readLoop(verbose bool) {
	sc := bufio.NewScanner(b.port)
	sc.Buffer(make([]byte, 0, 4096), 4096)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		// Diagnostics. The device logs on the same wire, because the DK has
		// three VCOM ports and needing two of them for one device is a worse
		// trade than one reserved character.
		if strings.HasPrefix(line, "#") {
			if verbose {
				fmt.Printf("  \033[90m%s\033[0m\n", line)
			}
			continue
		}

		if !strings.HasPrefix(line, "PUB ") {
			// Boot banners, stray output, line noise after a reset. Counted
			// rather than logged, so a noisy cable does not bury real traffic.
			b.dropped++
			if verbose {
				fmt.Printf("  \033[33m? %s\033[0m\n", line)
			}
			continue
		}

		rest := line[4:]
		sp := strings.IndexByte(rest, ' ')
		if sp < 0 {
			b.dropped++
			continue
		}
		suffix, value := rest[:sp], rest[sp+1:]

		// The announcement is not under the device's own prefix — enrollment
		// watches one well-known topic across the whole fleet.
		topic := b.device + "/" + suffix
		retain := false
		if suffix == "announce" {
			topic = "torpor/announce/" + b.device
			retain = true
		}
		if suffix == "status" {
			// Retained, so a controller starting later still learns the
			// device is alive. A device that booted before anyone was
			// listening has still announced itself.
			retain = true
		}

		b.cli.Publish(topic, 0, retain, value)
		b.up++
		if verbose {
			fmt.Printf("  \033[32m↑\033[0m %-44s %s\n", topic, trunc(value, 60))
		}
	}

	if err := sc.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "\n  serial read: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "\n  serial closed — the device is now unreachable\n")
	b.cli.Publish(b.device+"/status", 0, true, "offline").WaitTimeout(2 * time.Second)
}

// onCommand is MQTT -> device.
func (b *bridge) onCommand(_ mqtt.Client, m mqtt.Message) {
	suffix := strings.TrimPrefix(m.Topic(), b.device+"/")
	line := fmt.Sprintf("SUB %s %s\n", suffix, string(m.Payload()))

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.port.Write([]byte(line)); err != nil {
		fmt.Fprintf(os.Stderr, "  write: %v\n", err)
		return
	}
	b.down++
	fmt.Printf("  \033[36m↓\033[0m %-44s %s\n", m.Topic(), trunc(string(m.Payload()), 60))
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func die(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "torpor-bridge: "+f+"\n", a...)
	os.Exit(1)
}
