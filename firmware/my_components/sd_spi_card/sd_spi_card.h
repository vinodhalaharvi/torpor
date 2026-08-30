#pragma once

#ifdef USE_ESP32

#include "esphome/core/component.h"
#include "esphome/core/automation.h"
#include "esphome/core/helpers.h"
#include "esphome/components/sensor/sensor.h"

#include <string>

#include "driver/sdspi_host.h"
#include "driver/spi_common.h"
#include "esp_vfs_fat.h"
#include "sdmmc_cmd.h"

#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"

namespace esphome {
namespace sd_spi_card {

// One queued write. Fixed-size so the queue needs no heap allocation — an
// important property on an MCU where fragmentation kills long-running devices.
static const size_t SD_PATH_MAX = 64;
static const size_t SD_DATA_MAX = 192;

enum class WriteMode : uint8_t { APPEND, TRUNCATE, DELETE };

struct SDWriteRequest {
  char path[SD_PATH_MAX];
  char data[SD_DATA_MAX];
  uint16_t len;
  WriteMode mode;
};

class SDSPICard : public Component {
 public:
  void setup() override;
  void loop() override;
  void dump_config() override;
  float get_setup_priority() const override { return setup_priority::DATA; }

  // --- setters, called from generated code -------------------------------
  void set_clk_pin(uint8_t p) { this->clk_pin_ = p; }
  void set_mosi_pin(uint8_t p) { this->mosi_pin_ = p; }
  void set_miso_pin(uint8_t p) { this->miso_pin_ = p; }
  void set_cs_pin(uint8_t p) { this->cs_pin_ = p; }
  void set_mount_point(const std::string &m) { this->mount_point_ = m; }
  void set_format_if_mount_failed(bool f) { this->format_if_mount_failed_ = f; }
  void set_max_files(uint8_t n) { this->max_files_ = n; }
  void set_frequency_khz(uint32_t khz) { this->frequency_khz_ = khz; }
  void set_queue_depth(uint8_t d) { this->queue_depth_ = d; }

  void set_total_space_sensor(sensor::Sensor *s) { this->total_space_sensor_ = s; }
  void set_used_space_sensor(sensor::Sensor *s) { this->used_space_sensor_ = s; }
  void set_free_space_sensor(sensor::Sensor *s) { this->free_space_sensor_ = s; }

  // --- public API, safe to call from lambdas ------------------------------
  // These ENQUEUE work; they do not touch the card. They return false only if
  // the queue is full or the card is not mounted.
  bool append_file(const std::string &path, const std::string &data);
  bool write_file(const std::string &path, const std::string &data);
  bool delete_file(const std::string &path);

  bool is_mounted() const { return this->mounted_; }
  uint32_t get_dropped_writes() const { return this->dropped_; }

  // Blocking. Only call from a lambda you are happy to have stall.
  size_t file_size(const std::string &path);

 protected:
  bool mount_();
  void update_space_();
  bool enqueue_(const std::string &path, const std::string &data, WriteMode mode);
  void service_queue_();               // runs on the writer task
  void handle_request_(const SDWriteRequest &req);
  std::string full_path_(const std::string &path) const;

  static void writer_task_trampoline(void *param);

  uint8_t clk_pin_{0}, mosi_pin_{0}, miso_pin_{0}, cs_pin_{0};
  std::string mount_point_{"/sdcard"};
  bool format_if_mount_failed_{false};
  uint8_t max_files_{5};
  uint32_t frequency_khz_{10000};
  uint8_t queue_depth_{16};

  bool mounted_{false};
  uint32_t dropped_{0};
  uint32_t last_space_update_{0};

  sdmmc_card_t *card_{nullptr};
  spi_host_device_t spi_host_{SPI2_HOST};

  QueueHandle_t queue_{nullptr};
  TaskHandle_t writer_task_{nullptr};

  sensor::Sensor *total_space_sensor_{nullptr};
  sensor::Sensor *used_space_sensor_{nullptr};
  sensor::Sensor *free_space_sensor_{nullptr};
};

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------
template<typename... Ts> class AppendFileAction : public Action<Ts...>, public Parented<SDSPICard> {
 public:
  TEMPLATABLE_VALUE(std::string, path)
  TEMPLATABLE_VALUE(std::string, data)
  void play(const Ts &...x) override {
    this->parent_->append_file(this->path_.value(x...), this->data_.value(x...));
  }
};

template<typename... Ts> class WriteFileAction : public Action<Ts...>, public Parented<SDSPICard> {
 public:
  TEMPLATABLE_VALUE(std::string, path)
  TEMPLATABLE_VALUE(std::string, data)
  void play(const Ts &...x) override {
    this->parent_->write_file(this->path_.value(x...), this->data_.value(x...));
  }
};

template<typename... Ts> class DeleteFileAction : public Action<Ts...>, public Parented<SDSPICard> {
 public:
  TEMPLATABLE_VALUE(std::string, path)
  void play(const Ts &...x) override { this->parent_->delete_file(this->path_.value(x...)); }
};

}  // namespace sd_spi_card
}  // namespace esphome

#endif  // USE_ESP32
