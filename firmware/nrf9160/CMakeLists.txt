cmake_minimum_required(VERSION 3.20.0)
find_package(Zephyr REQUIRED HINTS $ENV{ZEPHYR_BASE})
project(torpor_nrf9160)

target_sources(app PRIVATE src/main.c)

# Identity, baked at build time the way ESPHome's substitutions are.
#
# Defaults matter here, and not only as convenience. `west build -- -DTORPOR_ID=x`
# passes the flag to the SYSBUILD scope, one level above this image, so it never
# arrives — and CMake then expands ${TORPOR_ID} to an empty string rather than
# leaving the #ifndef default in main.c to take over.
#
# The result was a device publishing an empty running_config_hash: the one
# contract item that is not optional, silently blank. Caught by reading the
# serial output, not by anything failing.
#
# Per-image override is -Dnrf9160_TORPOR_ID=... — the image name prefixes it.
if(NOT TORPOR_ID)
  set(TORPOR_ID "nrf-01")
endif()
if(NOT TORPOR_HASH)
  set(TORPOR_HASH "a1b2c3d")
endif()

target_compile_definitions(app PRIVATE
  DEVICE_ID="${TORPOR_ID}"
  FIRMWARE_HASH="${TORPOR_HASH}"
  DEVICE_MODEL="nrf9160dk"
)
