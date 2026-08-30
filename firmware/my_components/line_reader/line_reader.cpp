#include "line_reader.h"
#include "esphome/core/log.h"

namespace esphome {
namespace line_reader {

// Every ESPHome component defines a TAG for its log lines. This is what you
// filter on with `logs: line_reader: VERY_VERBOSE` in YAML.
static const char *const TAG = "line_reader";

void LineReader::setup() {
  // Reserve up front so we never reallocate inside loop().
  this->buffer_.reserve(this->max_line_length_);
  ESP_LOGCONFIG(TAG, "Setting up Line Reader...");
}

void LineReader::loop() {
  // loop() is called continuously. It MUST NOT block — no delay(), no
  // while(waiting). Drain whatever is available and return immediately.
  while (this->available()) {
    uint8_t c;
    if (!this->read_byte(&c))
      break;

    // Ignore CR so we handle both \n and \r\n line endings
    if (c == '\r')
      continue;

    if (c == '\n') {
      if (!this->buffer_.empty()) {
        this->publish_line_(this->buffer_);
        this->buffer_.clear();
      }
      continue;
    }

    // Defensive: a peer that never sends a newline would otherwise grow this
    // buffer until the heap is gone. Bounded input is not optional on MCUs.
    if (this->buffer_.size() >= this->max_line_length_) {
      ESP_LOGW(TAG, "Line exceeded %u bytes, discarding", (unsigned) this->max_line_length_);
      this->buffer_.clear();
      continue;
    }

    this->buffer_.push_back((char) c);
  }
}

void LineReader::publish_line_(const std::string &line) {
  this->lines_received_++;
  ESP_LOGD(TAG, "RX (%u bytes): %s", (unsigned) line.size(), line.c_str());

  if (this->last_line_sensor_ != nullptr)
    this->last_line_sensor_->publish_state(line);

  // Fan out to every registered on_line: automation
  this->line_callback_.call(line);
}

void LineReader::send_line(const std::string &line) {
  ESP_LOGD(TAG, "TX: %s", line.c_str());
  this->write_str(line.c_str());
  this->write_byte('\n');
}

void LineReader::dump_config() {
  // dump_config() runs once at boot and prints your configuration back.
  // Every built-in component does this — it is how you verify what actually
  // got compiled in, as opposed to what you think you wrote in YAML.
  ESP_LOGCONFIG(TAG, "Line Reader:");
  ESP_LOGCONFIG(TAG, "  Max line length: %u", (unsigned) this->max_line_length_);
  LOG_TEXT_SENSOR("  ", "Last Line", this->last_line_sensor_);
  this->check_uart_settings(115200);
}

}  // namespace line_reader
}  // namespace esphome
