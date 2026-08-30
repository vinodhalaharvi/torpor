#include "sd_spi_card.h"

#ifdef USE_ESP32

#include "esphome/core/log.h"
#include <cstring>
#include <cstdio>
#include <sys/stat.h>
#include <sys/unistd.h>

namespace esphome {
namespace sd_spi_card {

static const char *const TAG = "sd_spi_card";

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------
void SDSPICard::setup() {
  ESP_LOGCONFIG(TAG, "Setting up SD card over SPI...");

  this->queue_ = xQueueCreate(this->queue_depth_, sizeof(SDWriteRequest));
  if (this->queue_ == nullptr) {
    ESP_LOGE(TAG, "Failed to allocate write queue");
    this->mark_failed();
    return;
  }

  if (!this->mount_()) {
    // Do not mark_failed(): a missing card should not take the whole device
    // down. Everything else keeps running and enqueue_ simply returns false.
    ESP_LOGE(TAG, "SD card not mounted — writes will be dropped");
    return;
  }

  // 4 kB stack: FATFS plus stdio needs more than the default.
  BaseType_t ok = xTaskCreate(&SDSPICard::writer_task_trampoline, "sd_writer", 4096, this,
                              tskIDLE_PRIORITY + 1, &this->writer_task_);
  if (ok != pdPASS) {
    ESP_LOGE(TAG, "Failed to start writer task");
    this->mark_failed();
    return;
  }

  this->update_space_();
}

void SDSPICard::loop() {
  // The main loop does nothing expensive. All card access is on the task.
  // Space figures are cheap-ish but still touch the filesystem, so throttle.
  const uint32_t now = millis();
  if (this->mounted_ && now - this->last_space_update_ > 60000) {
    this->last_space_update_ = now;
    this->update_space_();
  }
}

void SDSPICard::dump_config() {
  ESP_LOGCONFIG(TAG, "SD Card (SPI):");
  ESP_LOGCONFIG(TAG, "  CLK Pin: GPIO%u", this->clk_pin_);
  ESP_LOGCONFIG(TAG, "  MOSI Pin: GPIO%u", this->mosi_pin_);
  ESP_LOGCONFIG(TAG, "  MISO Pin: GPIO%u", this->miso_pin_);
  ESP_LOGCONFIG(TAG, "  CS Pin: GPIO%u", this->cs_pin_);
  ESP_LOGCONFIG(TAG, "  Mount point: %s", this->mount_point_.c_str());
  ESP_LOGCONFIG(TAG, "  Frequency: %u kHz", this->frequency_khz_);
  ESP_LOGCONFIG(TAG, "  Queue depth: %u", this->queue_depth_);
  ESP_LOGCONFIG(TAG, "  Mounted: %s", YESNO(this->mounted_));

  if (this->mounted_ && this->card_ != nullptr) {
    ESP_LOGCONFIG(TAG, "  Card name: %s", this->card_->cid.name);
    ESP_LOGCONFIG(TAG, "  Card size: %llu MB",
                  ((uint64_t) this->card_->csd.capacity) * this->card_->csd.sector_size / (1024 * 1024));
  }
}

// ---------------------------------------------------------------------------
// Mounting
// ---------------------------------------------------------------------------
bool SDSPICard::mount_() {
  esp_err_t ret;

  sdmmc_host_t host = SDSPI_HOST_DEFAULT();
  host.slot = this->spi_host_;
  host.max_freq_khz = (int) this->frequency_khz_;

  spi_bus_config_t bus_cfg = {};
  bus_cfg.mosi_io_num = this->mosi_pin_;
  bus_cfg.miso_io_num = this->miso_pin_;
  bus_cfg.sclk_io_num = this->clk_pin_;
  bus_cfg.quadwp_io_num = -1;
  bus_cfg.quadhd_io_num = -1;
  bus_cfg.max_transfer_sz = 4000;

  ret = spi_bus_initialize(this->spi_host_, &bus_cfg, SDSPI_DEFAULT_DMA);
  if (ret != ESP_OK) {
    ESP_LOGE(TAG, "spi_bus_initialize failed: %s", esp_err_to_name(ret));
    return false;
  }

  sdspi_device_config_t slot_config = SDSPI_DEVICE_CONFIG_DEFAULT();
  slot_config.gpio_cs = (gpio_num_t) this->cs_pin_;
  slot_config.host_id = this->spi_host_;

  esp_vfs_fat_sdmmc_mount_config_t mount_config = {};
  mount_config.format_if_mount_failed = this->format_if_mount_failed_;
  mount_config.max_files = this->max_files_;
  mount_config.allocation_unit_size = 16 * 1024;

  ret = esp_vfs_fat_sdspi_mount(this->mount_point_.c_str(), &host, &slot_config, &mount_config,
                                &this->card_);
  if (ret != ESP_OK) {
    if (ret == ESP_FAIL) {
      ESP_LOGE(TAG, "Mount failed. Card may not be FAT-formatted. "
                    "Set format_if_mount_failed: true to format it (destroys data).");
    } else {
      ESP_LOGE(TAG, "Card init failed: %s. Check wiring and that the card is seated.",
               esp_err_to_name(ret));
    }
    spi_bus_free(this->spi_host_);
    return false;
  }

  this->mounted_ = true;
  ESP_LOGI(TAG, "SD card mounted at %s", this->mount_point_.c_str());
  return true;
}

// ---------------------------------------------------------------------------
// Public API — these only enqueue
// ---------------------------------------------------------------------------
bool SDSPICard::append_file(const std::string &path, const std::string &data) {
  return this->enqueue_(path, data, WriteMode::APPEND);
}

bool SDSPICard::write_file(const std::string &path, const std::string &data) {
  return this->enqueue_(path, data, WriteMode::TRUNCATE);
}

bool SDSPICard::delete_file(const std::string &path) {
  return this->enqueue_(path, "", WriteMode::DELETE);
}

bool SDSPICard::enqueue_(const std::string &path, const std::string &data, WriteMode mode) {
  if (!this->mounted_ || this->queue_ == nullptr) {
    this->dropped_++;
    return false;
  }
  if (path.size() >= SD_PATH_MAX || data.size() >= SD_DATA_MAX) {
    ESP_LOGW(TAG, "Path or data too long (max %u / %u), dropping",
             (unsigned) SD_PATH_MAX - 1, (unsigned) SD_DATA_MAX - 1);
    this->dropped_++;
    return false;
  }

  SDWriteRequest req = {};
  strncpy(req.path, path.c_str(), SD_PATH_MAX - 1);
  memcpy(req.data, data.c_str(), data.size());
  req.len = (uint16_t) data.size();
  req.mode = mode;

  // Zero timeout: never block the caller. A full queue means the card cannot
  // keep up, and dropping is better than stalling the main loop.
  if (xQueueSend(this->queue_, &req, 0) != pdTRUE) {
    this->dropped_++;
    ESP_LOGW(TAG, "Write queue full, dropped (total dropped: %u)", this->dropped_);
    return false;
  }
  return true;
}

// ---------------------------------------------------------------------------
// Writer task
// ---------------------------------------------------------------------------
void SDSPICard::writer_task_trampoline(void *param) {
  static_cast<SDSPICard *>(param)->service_queue_();
}

void SDSPICard::service_queue_() {
  SDWriteRequest req;
  for (;;) {
    if (xQueueReceive(this->queue_, &req, portMAX_DELAY) == pdTRUE) {
      this->handle_request_(req);
    }
  }
}

void SDSPICard::handle_request_(const SDWriteRequest &req) {
  const std::string path = this->full_path_(req.path);

  if (req.mode == WriteMode::DELETE) {
    if (::remove(path.c_str()) != 0)
      ESP_LOGW(TAG, "Failed to delete %s", path.c_str());
    else
      ESP_LOGD(TAG, "Deleted %s", path.c_str());
    return;
  }

  const char *fmode = (req.mode == WriteMode::APPEND) ? "a" : "w";
  FILE *f = fopen(path.c_str(), fmode);
  if (f == nullptr) {
    ESP_LOGE(TAG, "Failed to open %s for writing", path.c_str());
    return;
  }

  const size_t written = fwrite(req.data, 1, req.len, f);
  fclose(f);

  if (written != req.len) {
    ESP_LOGE(TAG, "Short write to %s: %u of %u bytes", path.c_str(), (unsigned) written,
             (unsigned) req.len);
  } else {
    ESP_LOGV(TAG, "Wrote %u bytes to %s", (unsigned) written, path.c_str());
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
std::string SDSPICard::full_path_(const std::string &path) const {
  if (!path.empty() && path[0] == '/')
    return this->mount_point_ + path;
  return this->mount_point_ + "/" + path;
}

size_t SDSPICard::file_size(const std::string &path) {
  if (!this->mounted_)
    return 0;
  struct stat st;
  if (stat(this->full_path_(path).c_str(), &st) != 0)
    return 0;
  return (size_t) st.st_size;
}

void SDSPICard::update_space_() {
  if (!this->mounted_ || this->card_ == nullptr)
    return;

  FATFS *fs;
  DWORD free_clusters;
  // The FATFS drive number that esp_vfs_fat_sdspi_mount registered.
  char drv[3] = {(char) ('0' + this->card_->host.slot), ':', 0};

  if (f_getfree(drv, &free_clusters, &fs) != FR_OK) {
    ESP_LOGW(TAG, "f_getfree failed");
    return;
  }

  const uint64_t sector = this->card_->csd.sector_size;
  const uint64_t total = ((uint64_t) (fs->n_fatent - 2)) * fs->csize * sector;
  const uint64_t freeb = ((uint64_t) free_clusters) * fs->csize * sector;

  if (this->total_space_sensor_ != nullptr)
    this->total_space_sensor_->publish_state((float) total);
  if (this->free_space_sensor_ != nullptr)
    this->free_space_sensor_->publish_state((float) freeb);
  if (this->used_space_sensor_ != nullptr)
    this->used_space_sensor_->publish_state((float) (total - freeb));
}

}  // namespace sd_spi_card
}  // namespace esphome

#endif  // USE_ESP32
