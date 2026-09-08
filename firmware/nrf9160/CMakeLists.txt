cmake_minimum_required(VERSION 3.20.0)
find_package(Zephyr REQUIRED HINTS $ENV{ZEPHYR_BASE})
project(torpor_nrf9160)

target_sources(app PRIVATE src/main.c)

# Identity, baked at build time the way ESPHome's substitutions are.
# Override per device:
#   west build ... -- -DCONFIG_TORPOR_ID=nrf-02
target_compile_definitions(app PRIVATE
  DEVICE_ID="${TORPOR_ID}"
  FIRMWARE_HASH="${TORPOR_HASH}"
  DEVICE_MODEL="nrf9160dk"
)
