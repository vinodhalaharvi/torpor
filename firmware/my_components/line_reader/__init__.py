"""
ESPHome external component: line_reader

This file is the CONFIG LAYER. It does three jobs:
  1. Declares what YAML keys are legal (CONFIG_SCHEMA)
  2. Declares the C++ types that exist (cg.esphome_ns.namespace / .class_)
  3. Emits C++ constructor calls based on the user's YAML (to_code)

ESPHome never "interprets" your YAML at runtime. This Python runs at COMPILE
time and writes C++ into main.cpp. That is the whole trick.
"""

import esphome.codegen as cg
import esphome.config_validation as cv
from esphome import automation
from esphome.components import text_sensor, uart
from esphome.const import CONF_ID, CONF_TRIGGER_ID

# "uart" must be configured before us. ESPHome enforces this.
DEPENDENCIES = ["uart"]
# We emit a text_sensor, so pull that platform in automatically.
AUTO_LOAD = ["text_sensor"]

# ---------------------------------------------------------------------------
# Mirror the C++ namespace and classes into Python so codegen can name them.
# These strings must match line_reader.h exactly.
# ---------------------------------------------------------------------------
line_reader_ns = cg.esphome_ns.namespace("line_reader")

LineReader = line_reader_ns.class_("LineReader", cg.Component, uart.UARTDevice)
LineTrigger = line_reader_ns.class_(
    "LineTrigger", automation.Trigger.template(cg.std_string)
)
SendLineAction = line_reader_ns.class_("SendLineAction", automation.Action)

# ---------------------------------------------------------------------------
# Custom YAML keys
# ---------------------------------------------------------------------------
CONF_ON_LINE = "on_line"
CONF_LAST_LINE = "last_line"
CONF_MAX_LINE_LENGTH = "max_line_length"
CONF_LINE = "line"

CONFIG_SCHEMA = (
    cv.Schema(
        {
            # Every component needs an id so lambdas can reach it
            cv.GenerateID(): cv.declare_id(LineReader),
            # Optional: expose the most recent line as a text sensor
            cv.Optional(CONF_LAST_LINE): text_sensor.text_sensor_schema(),
            # Guard against a peer that never sends '\n' and eats all our RAM
            cv.Optional(CONF_MAX_LINE_LENGTH, default=256): cv.int_range(
                min=16, max=2048
            ),
            # Optional: run an automation every time a line arrives
            cv.Optional(CONF_ON_LINE): automation.validate_automation(
                {
                    cv.GenerateID(CONF_TRIGGER_ID): cv.declare_id(LineTrigger),
                }
            ),
        }
    )
    .extend(cv.COMPONENT_SCHEMA)
    .extend(uart.UART_DEVICE_SCHEMA)
)


async def to_code(config):
    """Emit the C++ that instantiates and wires up our component."""
    # cg.new_Pvariable emits:  LineReader *my_lines = new LineReader();
    var = cg.new_Pvariable(config[CONF_ID])

    # Registers setup()/loop() with the ESPHome scheduler
    await cg.register_component(var, config)
    # Wires the uart_id: from YAML into our UARTDevice base class
    await uart.register_uart_device(var, config)

    # Emits:  my_lines->set_max_line_length(256);
    cg.add(var.set_max_line_length(config[CONF_MAX_LINE_LENGTH]))

    if CONF_LAST_LINE in config:
        sens = await text_sensor.new_text_sensor(config[CONF_LAST_LINE])
        cg.add(var.set_last_line_sensor(sens))

    # For each on_line: block, build a trigger and attach the user's actions.
    # The (std_string, "x") tuple is what makes `x` available in their lambdas.
    for conf in config.get(CONF_ON_LINE, []):
        trigger = cg.new_Pvariable(conf[CONF_TRIGGER_ID], var)
        await automation.build_automation(trigger, [(cg.std_string, "x")], conf)


# ---------------------------------------------------------------------------
# Register the `line_reader.send_line:` action so it can be used anywhere
# an automation action is accepted (on_press, on_boot, interval, etc.)
# ---------------------------------------------------------------------------
@automation.register_action(
    "line_reader.send_line",
    SendLineAction,
    cv.Schema(
        {
            cv.GenerateID(): cv.use_id(LineReader),
            cv.Required(CONF_LINE): cv.templatable(cv.string),
        }
    ),
)
async def send_line_to_code(config, action_id, template_arg, args):
    var = cg.new_Pvariable(action_id, template_arg)
    # Parented<LineReader> gets its parent_ pointer set here
    await cg.register_parented(var, config[CONF_ID])
    # templatable() means users can pass a literal OR a lambda
    templ = await cg.templatable(config[CONF_LINE], args, cg.std_string)
    cg.add(var.set_line(templ))
    return var
