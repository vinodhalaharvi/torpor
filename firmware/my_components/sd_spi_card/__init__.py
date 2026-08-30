"""
ESPHome external component: sd_spi_card

Mounts a FAT-formatted SD card over SPI using ESP-IDF's sdspi_host driver.

WHY THIS EXISTS
---------------
The existing community component (n-serrette/esphome_sd_card) is built on the
SDMMC host peripheral, which only the original ESP32 and the ESP32-S3 have.
The C3, C6, H2 and S2 have no SDMMC controller at all and can only reach an SD
card over SPI. This component fills that gap — and works on the S3/ESP32 too.

DESIGN NOTES
------------
- This component owns its own SPI bus. It does NOT use ESPHome's `spi:`
  component, because ESP-IDF's sdspi_host wants to call spi_bus_initialize()
  itself. Sharing a bus with other SPI devices is possible in IDF but adds
  locking requirements; that is deliberately out of scope for v1.
- Writes happen on a dedicated FreeRTOS task fed by a queue, so a slow card
  never stalls ESPHome's main loop.
"""

import esphome.codegen as cg
import esphome.config_validation as cv
from esphome import automation, pins
from esphome.components import sensor
from esphome.const import (
    CONF_ID,
    CONF_CS_PIN,
    CONF_MISO_PIN,
    CONF_MOSI_PIN,
    CONF_PATH,
    DEVICE_CLASS_DATA_SIZE,
    ENTITY_CATEGORY_DIAGNOSTIC,
    STATE_CLASS_MEASUREMENT,
    UNIT_BYTES,
)

CODEOWNERS = ["@you"]
DEPENDENCIES = ["esp32"]
AUTO_LOAD = ["sensor"]

sd_spi_ns = cg.esphome_ns.namespace("sd_spi_card")
SDSPICard = sd_spi_ns.class_("SDSPICard", cg.Component)

AppendFileAction = sd_spi_ns.class_("AppendFileAction", automation.Action)
WriteFileAction = sd_spi_ns.class_("WriteFileAction", automation.Action)
DeleteFileAction = sd_spi_ns.class_("DeleteFileAction", automation.Action)

CONF_CLK_PIN = "clk_pin"
CONF_MOUNT_POINT = "mount_point"
CONF_FORMAT_IF_MOUNT_FAILED = "format_if_mount_failed"
CONF_MAX_FILES = "max_files"
CONF_FREQUENCY_KHZ = "frequency_khz"
CONF_QUEUE_DEPTH = "queue_depth"
CONF_TOTAL_SPACE = "total_space"
CONF_USED_SPACE = "used_space"
CONF_FREE_SPACE = "free_space"
CONF_DATA = "data"

_SPACE_SENSOR = sensor.sensor_schema(
    unit_of_measurement=UNIT_BYTES,
    accuracy_decimals=0,
    device_class=DEVICE_CLASS_DATA_SIZE,
    state_class=STATE_CLASS_MEASUREMENT,
    entity_category=ENTITY_CATEGORY_DIAGNOSTIC,
)

CONFIG_SCHEMA = cv.Schema(
    {
        cv.GenerateID(): cv.declare_id(SDSPICard),
        cv.Required(CONF_CLK_PIN): pins.internal_gpio_output_pin_number,
        cv.Required(CONF_MOSI_PIN): pins.internal_gpio_output_pin_number,
        cv.Required(CONF_MISO_PIN): pins.internal_gpio_input_pin_number,
        cv.Required(CONF_CS_PIN): pins.internal_gpio_output_pin_number,
        cv.Optional(CONF_MOUNT_POINT, default="/sdcard"): cv.string_strict,
        cv.Optional(CONF_FORMAT_IF_MOUNT_FAILED, default=False): cv.boolean,
        cv.Optional(CONF_MAX_FILES, default=5): cv.int_range(min=1, max=20),
        # SD-over-SPI cannot exceed SDMMC_FREQ_DEFAULT (20 MHz). Start slow;
        # long jumper wires will not survive 20 MHz.
        cv.Optional(CONF_FREQUENCY_KHZ, default=10000): cv.int_range(
            min=400, max=20000
        ),
        cv.Optional(CONF_QUEUE_DEPTH, default=16): cv.int_range(min=4, max=64),
        cv.Optional(CONF_TOTAL_SPACE): _SPACE_SENSOR,
        cv.Optional(CONF_USED_SPACE): _SPACE_SENSOR,
        cv.Optional(CONF_FREE_SPACE): _SPACE_SENSOR,
    }
).extend(cv.COMPONENT_SCHEMA)


async def to_code(config):
    var = cg.new_Pvariable(config[CONF_ID])
    await cg.register_component(var, config)

    cg.add(var.set_clk_pin(config[CONF_CLK_PIN]))
    cg.add(var.set_mosi_pin(config[CONF_MOSI_PIN]))
    cg.add(var.set_miso_pin(config[CONF_MISO_PIN]))
    cg.add(var.set_cs_pin(config[CONF_CS_PIN]))
    cg.add(var.set_mount_point(config[CONF_MOUNT_POINT]))
    cg.add(var.set_format_if_mount_failed(config[CONF_FORMAT_IF_MOUNT_FAILED]))
    cg.add(var.set_max_files(config[CONF_MAX_FILES]))
    cg.add(var.set_frequency_khz(config[CONF_FREQUENCY_KHZ]))
    cg.add(var.set_queue_depth(config[CONF_QUEUE_DEPTH]))

    for key, setter in (
        (CONF_TOTAL_SPACE, var.set_total_space_sensor),
        (CONF_USED_SPACE, var.set_used_space_sensor),
        (CONF_FREE_SPACE, var.set_free_space_sensor),
    ):
        if key in config:
            sens = await sensor.new_sensor(config[key])
            cg.add(setter(sens))


# ---------------------------------------------------------------------------
# Actions
# ---------------------------------------------------------------------------
_FILE_ACTION_SCHEMA = cv.Schema(
    {
        cv.GenerateID(): cv.use_id(SDSPICard),
        cv.Required(CONF_PATH): cv.templatable(cv.string),
        cv.Required(CONF_DATA): cv.templatable(cv.string),
    }
)


@automation.register_action(
    "sd_spi_card.append_file", AppendFileAction, _FILE_ACTION_SCHEMA
)
async def append_file_to_code(config, action_id, template_arg, args):
    var = cg.new_Pvariable(action_id, template_arg)
    await cg.register_parented(var, config[CONF_ID])
    cg.add(var.set_path(await cg.templatable(config[CONF_PATH], args, cg.std_string)))
    cg.add(var.set_data(await cg.templatable(config[CONF_DATA], args, cg.std_string)))
    return var


@automation.register_action(
    "sd_spi_card.write_file", WriteFileAction, _FILE_ACTION_SCHEMA
)
async def write_file_to_code(config, action_id, template_arg, args):
    var = cg.new_Pvariable(action_id, template_arg)
    await cg.register_parented(var, config[CONF_ID])
    cg.add(var.set_path(await cg.templatable(config[CONF_PATH], args, cg.std_string)))
    cg.add(var.set_data(await cg.templatable(config[CONF_DATA], args, cg.std_string)))
    return var


@automation.register_action(
    "sd_spi_card.delete_file",
    DeleteFileAction,
    cv.Schema(
        {
            cv.GenerateID(): cv.use_id(SDSPICard),
            cv.Required(CONF_PATH): cv.templatable(cv.string),
        }
    ),
)
async def delete_file_to_code(config, action_id, template_arg, args):
    var = cg.new_Pvariable(action_id, template_arg)
    await cg.register_parented(var, config[CONF_ID])
    cg.add(var.set_path(await cg.templatable(config[CONF_PATH], args, cg.std_string)))
    return var
