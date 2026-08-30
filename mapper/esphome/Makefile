SHELL := /bin/bash

curr_dir := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
rest_args := $(wordlist 2, $(words $(MAKECMDGOALS)), $(MAKECMDGOALS))
$(eval $(rest_args):;@:)

help:
	#
	# Usage:
	#   make generate :  generate a mapper based on a template.
	#   make mapper {mapper-name} <action> <parameter>:  execute mapper building process.
	#
	# Actions:
	#   -           mod, m  :  download code dependencies.
	#   -          lint, l  :  verify code via go fmt and `golangci-lint`.
	#   -         build, b  :  compile code.
	#   -       package, p  :  package docker image.
	#   -         clean, c  :  clean output binary.
	#
	# Parameters:
	#   ARM   : true or undefined
	#   ARM64 : true or undefined
	#
	# Example:
	#   -  make mapper modbus ARM64=true :  execute `build` "modbus" mapper for ARM64.
	#   -        make mapper modbus test :  execute `test` "modbus" mapper.
	@echo

make_rules := $(shell ls $(curr_dir)/hack/make-rules | sed 's/.sh//g')
$(make_rules):
	@$(curr_dir)/hack/make-rules/$@.sh $(rest_args)

.DEFAULT_GOAL := help
.PHONY: $(make_rules) build test package


W10A_IP ?= 192.168.68.115
W10B_IP ?= 192.168.68.116     # fill in once B is flashed

flash-a: ## OTA flash w10-a
	cd firmware && esphome run w10-msg-a.yaml --device $(W10A_IP)

flash-b: ## OTA flash w10-b
	cd firmware && esphome run w10-msg-b.yaml --device $(W10B_IP)