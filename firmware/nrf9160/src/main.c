/*
 * torpor device contract, on an nRF9160 with no network.
 *
 * docs/device-contract.md is five items, four of which are publishing a
 * string. This publishes those strings over UART instead of MQTT, one per
 * line, and a host-side bridge republishes them. Nothing above the mapper
 * knows the difference — which is the claim the contract makes and this file
 * is the first test of it against firmware that is not ESPHome.
 *
 * Line protocol, deliberately not JSON:
 *
 *     PUB <topic-suffix> <value>
 *     SUB <topic-suffix> <value>      (from the host, to us)
 *
 * Serial output gets corrupted by resets and line noise. A malformed line
 * here costs one reading; a malformed JSON object costs a parse error and a
 * decision about what to do with it. This is also readable in `tio` without
 * a tool, which matters more during bring-up than elegance does.
 *
 * Build (secure mode — the DK has no bootloader chain after an ERASEALL,
 * and without a SIM there is nothing for TrustZone to protect):
 *
 *     west build -p -b nrf9160dk/nrf9160 firmware/nrf9160 -d /tmp/torpor
 *     west flash -d /tmp/torpor
 */

#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/drivers/uart.h>
#include <zephyr/sys/printk.h>
#include <zephyr/random/random.h>
#include <string.h>
#include <stdlib.h>

/* Identity. Baked at build time, the way ESPHome's substitutions are.
 *
 * FIRMWARE_HASH must describe what is running rather than echo an
 * instruction. A device that reports the hash it was told proves nothing —
 * that failure happened on real hardware in this project, with a tone that
 * never played while every signal said success.
 *
 * On a board with MCUboot this should be the image header hash, which is
 * computed from the image rather than asserted about it. Here it is a build
 * constant, which is the same guarantee ESPHome gives.
 */
#ifndef DEVICE_ID
#define DEVICE_ID "nrf-01"
#endif
#ifndef FIRMWARE_HASH
#define FIRMWARE_HASH "a1b2c3d"
#endif
#ifndef DEVICE_MODEL
#define DEVICE_MODEL "nrf9160dk"
#endif

#define REPORT_INTERVAL_MS 10000

static const struct device *uart;

/* Everything the device says goes through here, so the wire format lives in
 * one place. */
static void pub(const char *suffix, const char *value)
{
	char line[192];
	int n = snprintf(line, sizeof(line), "PUB %s %s\r\n", suffix, value);

	for (int i = 0; i < n; i++) {
		uart_poll_out(uart, line[i]);
	}
}

/* --- contract item 5: accept a firmware URL and pull ---------------------
 *
 * Not implemented here, and the refusal is the point.
 *
 * Pulling firmware on this platform means MCUmgr over SMP, which is real work
 * and is where the interesting nRF capability lives — SMP over BLE or Thread
 * is the path DFU actually takes on Nordic parts. Until that exists this
 * device is monitorable and not updatable, which `torpor verify` will report
 * as 4 of 5 rather than as a failure.
 *
 * It still echoes the command, because the contract asks for the echo so a
 * controller can distinguish "received" from "acted on". A device that echoes
 * only on success makes those two indistinguishable.
 */
static char last_firmware_token[64];

static void on_firmware_url(const char *value)
{
	/* Deduplicate at the device.
	 *
	 * The mapper writes desired state on every collect cycle, not on change.
	 * Without this guard a real implementation would re-flash every ten
	 * seconds — which is how this was discovered, with a tone replaying
	 * forever on a board that was working exactly as instructed.
	 */
	/* Echo first, unconditionally — before the dedup check, before the
	 * no-op check.
	 *
	 * The echo answers "did you receive this", and the guards below answer
	 * "did you act on it". Echoing only when acting collapses those into one
	 * signal, so a device correctly refusing a no-op looks identical to a
	 * device that is dead. torpor verify caught exactly that: 4 of 5 with
	 * "wrote to nrf-01/text/firmware_url/command and saw no state echo",
	 * against firmware that was working precisely as designed. */
	pub("text/firmware_url/state", value);

	if (strncmp(value, last_firmware_token, sizeof(last_firmware_token)) == 0) {
		return;
	}
	strncpy(last_firmware_token, value, sizeof(last_firmware_token) - 1);

	const char *bar = strchr(value, '|');
	if (!bar) {
		printk("# firmware_url malformed, want <hash>|<url>\n");
		return;
	}

	/* Refuse a no-op. Re-flashing a converged device is pure risk. */
	size_t hash_len = bar - value;
	if (hash_len == strlen(FIRMWARE_HASH) &&
	    strncmp(value, FIRMWARE_HASH, hash_len) == 0) {
		printk("# already running %s, nothing to do\n", FIRMWARE_HASH);
		return;
	}

	printk("# would flash %s — MCUmgr not implemented, staying on %s\n",
	       bar + 1, FIRMWARE_HASH);
}

/* --- host -> device ------------------------------------------------------ */

static void handle_line(char *line)
{
	if (strncmp(line, "SUB ", 4) != 0) {
		return;
	}
	char *suffix = line + 4;
	char *space = strchr(suffix, ' ');
	if (!space) {
		return;
	}
	*space = '\0';
	char *value = space + 1;

	if (strcmp(suffix, "text/firmware_url/command") == 0) {
		on_firmware_url(value);
	} else {
		/* Unknown command topics are echoed rather than dropped, so a
		 * controller writing to a property this firmware does not implement
		 * sees convergence rather than silence. Silence is indistinguishable
		 * from a dead device. */
		char state[128];
		size_t len = strlen(suffix);
		if (len > 8 && strcmp(suffix + len - 8, "/command") == 0) {
			snprintf(state, sizeof(state), "%.*s/state", (int)(len - 8), suffix);
			pub(state, value);
		}
	}
}

static void rx_thread(void *a, void *b, void *c)
{
	static char buf[192];
	size_t len = 0;
	unsigned char ch;

	while (1) {
		if (uart_poll_in(uart, &ch) == 0) {
			if (ch == '\n' || ch == '\r') {
				if (len > 0) {
					buf[len] = '\0';
					handle_line(buf);
					len = 0;
				}
			} else if (len < sizeof(buf) - 1) {
				buf[len++] = ch;
			} else {
				/* Overlong line. Drop it rather than truncating into
				 * something that parses as a different command. */
				len = 0;
			}
		} else {
			k_msleep(10);
		}
	}
}

K_THREAD_DEFINE(rx_tid, 1024, rx_thread, NULL, NULL, NULL, 7, 0, 0);

int main(void)
{
	uart = DEVICE_DT_GET(DT_CHOSEN(zephyr_console));
	if (!device_is_ready(uart)) {
		printk("uart not ready\n");
		return -1;
	}

	k_msleep(500);   /* let the host's serial bridge attach */

	/* --- contract item 3: retained birth ---------------------------------
	 * The bridge publishes this retained and sets the matching will, since
	 * only the bridge knows when the serial link drops. */
	pub("status", "online");

	/* --- contract item 4: announce ---------------------------------------
	 * Every field is a claim. Anyone with access to this serial port can
	 * assert any of it, which is why enrollment requires approval. */
	{
		char ann[256];
		snprintf(ann, sizeof(ann),
			 "{\"device\":\"%s\",\"model\":\"%s\",\"topicPrefix\":\"%s\","
			 "\"configHash\":\"%s\",\"firmwareVersion\":\"zephyr\","
			 "\"buildTime\":\"%s %s\"}",
			 DEVICE_ID, DEVICE_MODEL, DEVICE_ID, FIRMWARE_HASH,
			 __DATE__, __TIME__);
		pub("announce", ann);
	}

	while (1) {
		char val[32];

		/* --- item 1: sensor values --------------------------------------
		 * The DK has no SHT41. These are synthetic and labelled as such in
		 * the docs — the contract is about the shape of what a device
		 * publishes, and a real sensor changes one function, not the
		 * protocol. */
		int milli = 20000 + (sys_rand32_get() % 10000);
		snprintf(val, sizeof(val), "%d.%02d", milli / 1000, (milli % 1000) / 10);
		pub("sensor/temperature/state", val);

		int humid = 35000 + (sys_rand32_get() % 15000);
		snprintf(val, sizeof(val), "%d.%02d", humid / 1000, (humid % 1000) / 10);
		pub("sensor/humidity/state", val);

		/* --- item 2: what it is RUNNING ---------------------------------
		 * The one item that is not optional. A device that cannot report
		 * this has nothing for a health gate to check. */
		pub("text_sensor/running_config_hash/state", FIRMWARE_HASH);

		snprintf(val, sizeof(val), "%lld", k_uptime_get() / 1000);
		pub("sensor/uptime/state", val);

		/* Identity, every cycle rather than only at boot.
		 *
		 * MQTT has retention. A serial line does not. A device that
		 * announced itself before the bridge attached has, from the host's
		 * point of view, never announced itself at all — and there is no
		 * way to ask it again.
		 *
		 * The bridge republishes both of these retained, so the retention
		 * semantics the contract asks for do hold end to end. But it can
		 * only republish what it hears, which means the device has to keep
		 * saying it. Cheap: two lines every ten seconds.
		 *
		 * This is the one place where a serial device genuinely differs
		 * from an MQTT one, and it is worth the repetition rather than
		 * teaching the bridge to poll. */
		pub("status", "online");
		{
			char ann[256];
			snprintf(ann, sizeof(ann),
				 "{\"device\":\"%s\",\"model\":\"%s\",\"topicPrefix\":\"%s\","
				 "\"configHash\":\"%s\",\"firmwareVersion\":\"zephyr\","
				 "\"buildTime\":\"%s %s\"}",
				 DEVICE_ID, DEVICE_MODEL, DEVICE_ID, FIRMWARE_HASH,
				 __DATE__, __TIME__);
			pub("announce", ann);
		}

		k_msleep(REPORT_INTERVAL_MS);
	}
	return 0;
}
