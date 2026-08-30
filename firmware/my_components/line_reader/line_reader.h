#pragma once

#include "esphome/core/component.h"
#include "esphome/core/automation.h"
#include "esphome/core/helpers.h"
#include "esphome/components/uart/uart.h"
#include "esphome/components/text_sensor/text_sensor.h"

#include <string>

namespace esphome {
namespace line_reader {

/*
 * LineReader
 *
 * Inherits two things:
 *   Component      -> gives us setup() / loop() / dump_config() lifecycle
 *   uart::UARTDevice -> gives us available(), read_byte(), write_str()
 *
 * This is the standard shape of every ESPHome driver. Look at any built-in
 * component and you will find exactly this pattern.
 */
class LineReader : public Component, public uart::UARTDevice {
 public:
  // --- Component lifecycle -------------------------------------------------
  void setup() override;
  void loop() override;
  void dump_config() override;

  // Runs after hardware buses are up, before things that depend on data.
  float get_setup_priority() const override { return setup_priority::DATA; }

  // --- Setters called by generated code from __init__.py -------------------
  void set_max_line_length(size_t n) { this->max_line_length_ = n; }
  void set_last_line_sensor(text_sensor::TextSensor *s) { this->last_line_sensor_ = s; }

  // --- Public API usable from lambdas --------------------------------------
  void send_line(const std::string &line);
  uint32_t get_lines_received() const { return this->lines_received_; }

  // Triggers register themselves here so they get called on every line.
  void add_line_callback(std::function<void(std::string)> &&cb) {
    this->line_callback_.add(std::move(cb));
  }

 protected:
  void publish_line_(const std::string &line);

  std::string buffer_;
  size_t max_line_length_{256};
  uint32_t lines_received_{0};
  text_sensor::TextSensor *last_line_sensor_{nullptr};

  // CallbackManager is ESPHome's little observer-pattern helper.
  CallbackManager<void(std::string)> line_callback_;
};

/*
 * LineTrigger
 *
 * Bridges our callback into ESPHome's automation engine. The constructor
 * subscribes; trigger() fans out to whatever the user wrote under on_line:.
 * The <std::string> template parameter is what makes `x` a std::string
 * inside their lambdas.
 */
class LineTrigger : public Trigger<std::string> {
 public:
  explicit LineTrigger(LineReader *parent) {
    parent->add_line_callback([this](std::string line) { this->trigger(std::move(line)); });
  }
};

/*
 * SendLineAction
 *
 * Backs the `line_reader.send_line:` YAML action.
 * TEMPLATABLE_VALUE is an ESPHome macro that generates set_line() plus a
 * value accessor, so the user can pass either a literal string or a lambda.
 */
template<typename... Ts> class SendLineAction : public Action<Ts...>, public Parented<LineReader> {
 public:
  TEMPLATABLE_VALUE(std::string, line)

  void play(const Ts &...x) override { this->parent_->send_line(this->line_.value(x...)); }
};

}  // namespace line_reader
}  // namespace esphome
