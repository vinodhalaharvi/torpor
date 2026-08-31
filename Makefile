# ============================================================================
#  Fleet control plane on KubeEdge — ops Makefile
#
#  Day-to-day operations only. One-time installs (brew, lima template,
#  containerd, keadm) are deliberately not here.
#
#  Layout this assumes:
#    Mac            k3d cluster (CloudCore), mosquitto broker, kubectl
#    Lima VM "edge" edgecore + containerd + mapper
#    LAN            w10-a, w10-b publishing MQTT to the Mac
#
#  make            → help
#  make check      → is everything up?
#  make v0         → the acceptance test: print a temperature via kubectl
# ============================================================================

SHELL       := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# ESPHome's component cache is NOT concurrency-safe. Parallel builds corrupt it.
.NOTPARALLEL:

# ---------------------------------------------------------------------------
# Configuration — override on the command line: make devices DEVICE=w10-b
# ---------------------------------------------------------------------------

CLUSTER            ?= edge
K3S_IMAGE          ?= rancher/k3s:v1.32.5-k3s1
KUBEEDGE_VERSION   ?= v1.23.1

VM                 ?= edge
NODE               ?= lima-edge

DEVICE             ?= w10-a
PROPERTY           ?= temperature
NAMESPACE          ?= default

BROKER_PORT        ?= 1883
ESPHOME_DIR        ?= $(CURDIR)/firmware
ESPHOME_CONFIG     ?= w10-msg-a.yaml

# Board IPs. OTA by IP skips the interactive port menu and avoids mDNS, which
# collides with Lima's forwarded 5353 on this machine.
W10A_IP            ?= 192.168.68.115
W10B_IP            ?= 192.168.68.116

# The mapper lives in this repo. It was briefly a standalone checkout, and the
# stale default outlived that by a full day before anything called it.
MAPPER_DIR         ?= $(CURDIR)/mapper/esphome
MAPPER_BIN         ?= esphome-mapper
DMI_SOCKET         ?= /etc/kubeedge/dmi.sock

KUBECONFIG_VM      ?= /tmp/kubeconfig-vm.yaml

# Detect the Mac's LAN IP. This value appears in the CloudCore cert SANs, the
# ESPHome broker address and the keadm join flags — a mismatch is the single
# most common silent failure.
MAC_IP := $(shell ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true)

OK   := \033[0;32m✔\033[0m
BAD  := \033[0;31m✘\033[0m
WARN := \033[0;33m•\033[0m

.PHONY: help
help: ## Show this help
	@printf '\n  \033[1mFleet ops\033[0m — LAN IP: \033[36m$(MAC_IP)\033[0m  cluster: \033[36m$(CLUSTER)\033[0m  vm: \033[36m$(VM)\033[0m\n\n'
	@awk 'BEGIN {FS = ":.*?## "} \
	  /^# ---/ {next} \
	  /^##@/ {printf "\n  \033[1m%s\033[0m\n", substr($$0,5); next} \
	  /^[a-zA-Z0-9_.-]+:.*?##/ {printf "    \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf '\n'

.PHONY: guard-ip
guard-ip:
	@if [ -z "$(MAC_IP)" ]; then \
	  printf '$(BAD) No LAN IP found on en0/en1. Are you on a network?\n'; exit 1; fi

# ===========================================================================
##@ Health
# ===========================================================================

.PHONY: check
check: ## Full preflight — every hop in the chain
	@printf '\n  \033[1mMac\033[0m\n'
	@printf '    LAN IP        '; [ -n "$(MAC_IP)" ] && printf '$(OK) $(MAC_IP)\n' || printf '$(BAD) none\n'
	@printf '    docker        '; docker info >/dev/null 2>&1 && printf '$(OK)\n' || printf '$(BAD) Docker Desktop not running\n'
	@printf '    mosquitto     '; nc -z localhost $(BROKER_PORT) 2>/dev/null && printf '$(OK) :$(BROKER_PORT)\n' || printf '$(BAD) not listening\n'
	@printf '\n  \033[1mCluster\033[0m\n'
	@printf '    k3d           '; k3d cluster list $(CLUSTER) >/dev/null 2>&1 && printf '$(OK)\n' || printf '$(BAD) no cluster "$(CLUSTER)"\n'
	@printf '    apiserver     '; kubectl cluster-info >/dev/null 2>&1 && printf '$(OK)\n' || printf '$(BAD) unreachable\n'
	@printf '    cloudcore     '; kubectl -n kubeedge get pod -l k8s-app=kubeedge -o jsonpath='{.items[0].status.phase}' 2>/dev/null | grep -q Running && printf '$(OK)\n' || printf '$(BAD) not Running\n'
	@printf '    CRDs          '; kubectl get crd devices.devices.kubeedge.io >/dev/null 2>&1 && printf '$(OK)\n' || printf '$(BAD) device CRDs missing\n'
	@printf '\n  \033[1mEdge\033[0m\n'
	@printf '    vm            '; limactl list $(VM) --format '{{.Status}}' 2>/dev/null | grep -q Running && printf '$(OK)\n' || printf '$(BAD) not running\n'
	@printf '    edgecore      '; limactl shell $(VM) -- systemctl is-active edgecore 2>/dev/null | grep -q '^active' && printf '$(OK)\n' || printf '$(BAD) inactive\n'
	@printf '    node          '; kubectl get node $(NODE) -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | grep -q True && printf '$(OK) $(NODE)\n' || printf '$(BAD) $(NODE) not Ready\n'
	@printf '    dmi socket    '; limactl shell $(VM) -- test -S $(DMI_SOCKET) 2>/dev/null && printf '$(OK)\n' || printf '$(WARN) absent (expected until mapper runs)\n'
	@printf '\n  \033[1mDevice\033[0m\n'
	@printf '    mqtt $(DEVICE)   '; timeout 6 mosquitto_sub -h $(MAC_IP) -t '$(DEVICE)/#' -C 1 -W 5 >/dev/null 2>&1 && printf '$(OK) publishing\n' || printf '$(BAD) silent\n'
	@printf '\n'

.PHONY: v0
v0: guard-ip ## THE ACCEPTANCE TEST — a temperature, via kubectl
	@printf '  reading $(PROPERTY) from devicestatus/$(DEVICE)...\n\n'
	@v=$$(kubectl get devicestatus $(DEVICE) -n $(NAMESPACE) \
	      -o jsonpath='{.status.twins[?(@.propertyName=="$(PROPERTY)")].reported.value}' 2>/dev/null); \
	  if [ -z "$$v" ]; then \
	    printf '    $(BAD) no reported value — twins are empty\n\n'; \
	    printf '    try: kubectl get devicestatus $(DEVICE) -o yaml\n\n'; \
	    exit 1; \
	  fi; \
	  printf '    %s\n\n  $(OK) V0.\n\n' "$$v" 

.PHONY: dump
dump: ## Write a debug bundle to ./debug-<ts>/ for when something is wrong
	@d=debug-$$(date +%Y%m%d-%H%M%S); mkdir -p $$d; \
	kubectl get nodes -o wide            > $$d/nodes.txt 2>&1 || true; \
	kubectl -n kubeedge get all -o wide   > $$d/kubeedge.txt 2>&1 || true; \
	kubectl -n kubeedge logs -l k8s-app=kubeedge --tail=500 > $$d/cloudcore.log 2>&1 || true; \
	kubectl get devices,devicemodels -A -o yaml > $$d/devices.yaml 2>&1 || true; \
	limactl shell $(VM) -- sudo journalctl -u edgecore -n 500 --no-pager > $$d/edgecore.log 2>&1 || true; \
	limactl shell $(VM) -- cat /etc/kubeedge/config/edgecore.yaml > $$d/edgecore.yaml 2>&1 || true; \
	printf 'MAC_IP=$(MAC_IP)\nKUBEEDGE=$(KUBEEDGE_VERSION)\nK3S=$(K3S_IMAGE)\n' > $$d/env.txt; \
	printf '  $(OK) wrote %s\n' $$d

# ===========================================================================
##@ Cluster
# ===========================================================================

.PHONY: cluster-up
cluster-up: guard-ip ## Create the k3d cluster with CloudHub ports exposed
	k3d cluster create $(CLUSTER) --servers 1 \
	  --image $(K3S_IMAGE) \
	  --api-port 0.0.0.0:6443 \
	  -p "10000:10000@server:0" \
	  -p "10002:10002@server:0"
	kubectl get nodes

.PHONY: cluster-down
cluster-down: ## Stop the cluster (keeps state)
	k3d cluster stop $(CLUSTER)

.PHONY: cluster-start
cluster-start: ## Start a stopped cluster
	k3d cluster start $(CLUSTER)

.PHONY: cluster-nuke
cluster-nuke: ## Delete the cluster entirely
	k3d cluster delete $(CLUSTER)

.PHONY: kubeconfig-vm
kubeconfig-vm: guard-ip ## Emit a kubeconfig the VM can use, and push it in
	@k3d kubeconfig get $(CLUSTER) \
	  | sed 's#server: https://0\.0\.0\.0:#server: https://$(MAC_IP):#; s#server: https://127\.0\.0\.1:#server: https://$(MAC_IP):#' \
	  > $(KUBECONFIG_VM)
	@limactl copy $(KUBECONFIG_VM) $(VM):/tmp/kubeconfig.yaml
	@printf '  $(OK) VM has /tmp/kubeconfig.yaml pointing at $(MAC_IP):6443\n'

# ===========================================================================
##@ Broker
# ===========================================================================

.PHONY: broker-up broker-down broker-restart
broker-up: ## Start mosquitto
	brew services start mosquitto
broker-down: ## Stop mosquitto
	brew services stop mosquitto
broker-restart: ## Restart mosquitto (after a config change)
	brew services restart mosquitto

.PHONY: broker-tail
broker-tail: guard-ip ## Tail everything the boards are publishing
	mosquitto_sub -h $(MAC_IP) -t '#' -v

.PHONY: broker-device
broker-device: guard-ip ## Tail one device's topics
	mosquitto_sub -h $(MAC_IP) -t '$(DEVICE)/#' -v

.PHONY: broker-once
broker-once: guard-ip ## One message from the device, then exit (scriptable)
	timeout 10 mosquitto_sub -h $(MAC_IP) -t '$(DEVICE)/sensor/$(PROPERTY)/state' -C 1 -W 8

# ===========================================================================
##@ VM / edge node
# ===========================================================================

.PHONY: vm-up vm-down vm-shell
vm-up: ## Start the Lima VM
	limactl start $(VM)
vm-down: ## Stop the Lima VM
	limactl stop $(VM)
vm-shell: ## Shell into the VM
	limactl shell $(VM)

.PHONY: edge-status
edge-status: ## systemd status of edgecore
	limactl shell $(VM) -- systemctl status edgecore --no-pager || true

.PHONY: edge-logs
edge-logs: ## Follow edgecore logs
	limactl shell $(VM) -- sudo journalctl -u edgecore -f

.PHONY: edge-restart
edge-restart: ## Restart edgecore
	limactl shell $(VM) -- sudo systemctl restart edgecore
	@sleep 3 && $(MAKE) --no-print-directory edge-status

.PHONY: edge-config
edge-config: ## Print the live edgecore.yaml
	limactl shell $(VM) -- cat /etc/kubeedge/config/edgecore.yaml

.PHONY: edge-reset
edge-reset: ## keadm reset on the edge node — destructive, re-join after
	limactl shell $(VM) -- sudo keadm reset --force

.PHONY: token
token: ## Print a fresh join token
	@limactl shell $(VM) -- keadm gettoken --kubeconfig=/tmp/kubeconfig.yaml

.PHONY: join
join: guard-ip kubeconfig-vm ## Join the VM to the cluster as an edge node
	@t=$$(limactl shell $(VM) -- keadm gettoken --kubeconfig=/tmp/kubeconfig.yaml); \
	limactl shell $(VM) -- sudo keadm join \
	  --cloudcore-ipport=$(MAC_IP):10000 \
	  --token=$$t \
	  --kubeedge-version=$(KUBEEDGE_VERSION)
	@sleep 5 && kubectl get nodes

# ===========================================================================
##@ CloudCore
# ===========================================================================

.PHONY: cloudcore-logs
cloudcore-logs: ## Follow CloudCore logs
	kubectl -n kubeedge logs -l k8s-app=kubeedge -f --tail=100

.PHONY: cloudcore-restart
cloudcore-restart: ## Roll CloudCore
	kubectl -n kubeedge rollout restart deploy/cloudcore
	kubectl -n kubeedge rollout status deploy/cloudcore

.PHONY: cloudcore-status
cloudcore-status: ## Everything in the kubeedge namespace
	kubectl -n kubeedge get all -o wide

# ===========================================================================
##@ Devices
# ===========================================================================

.PHONY: devices
devices: ## List devices
	kubectl get devices -A -o wide

.PHONY: fleet
fleet: ## Every device, every twin, one table
	@printf '\n  %-8s %-18s %-12s %s\n' DEVICE PROPERTY REPORTED DESIRED
	@printf '  %-8s %-18s %-12s %s\n' -------- ------------------ ------------ -------
	@for d in $$(kubectl get devices -n $(NAMESPACE) -o jsonpath='{.items[*].metadata.name}'); do \
	  kubectl get devicestatus $$d -n $(NAMESPACE) -o jsonpath=\
'{range .status.twins[*]}{"'"$$d"'"}{"\t"}{.propertyName}{"\t"}{.reported.value}{"\t"}{.observedDesired.value}{"\n"}{end}' 2>/dev/null \
	    | awk -F'\t' '{printf "  %-8s %-18s %-12s %s\n", $$1, $$2, $$3, $$4}'; \
	done; printf '\n'

.PHONY: models
models: ## List device models
	kubectl get devicemodels -A

.PHONY: device
device: ## Describe one device (DEVICE=w10-a)
	kubectl describe device $(DEVICE) -n $(NAMESPACE)

.PHONY: twins
twins: ## Raw twin block for one device
	kubectl get devicestatus $(DEVICE) -n $(NAMESPACE) -o jsonpath='{.status.twins}' | jq .

.PHONY: watch
watch: ## Watch desired vs reported converge — the WANT/GOT view
	kubectl get devicestatus $(DEVICE) -n $(NAMESPACE) -w \
	  -o custom-columns='NAME:.metadata.name,WANT:.status.twins[0].observedDesired.value,GOT:.status.twins[0].reported.value'

.PHONY: converge
converge: ## Devices whose reported state has not caught up to desired
	@kubectl get devices -A -o json | jq -r ' \
	  .items[] \
	  | select(.spec.properties[]?.desired.value != \
	           (.status.twins[]? | select(.propertyName == "$(PROPERTY)") | .reported.value)) \
	  | .metadata.name' || true

.PHONY: apply
apply: ## Apply device manifests from ./manifests
	kubectl apply -f manifests/

.PHONY: v2
v2: ## The two-transport view — one device with an IP, one without
	@printf '\n  %-10s %-14s %-10s %-9s %s\n' NAME MODEL TRANSPORT LAST-SEEN TEMPERATURE
	@printf '  %-10s %-14s %-10s %-9s %s\n' ---------- -------------- ---------- --------- -----------
	@for d in $$(kubectl get devices -n $(NAMESPACE) -o jsonpath='{.items[*].metadata.name}'); do \
	  m=$$(kubectl get device $$d -n $(NAMESPACE) -o jsonpath='{.spec.deviceModelRef.name}'); \
	  t=$$(kubectl get device $$d -n $(NAMESPACE) -o jsonpath='{.metadata.labels.transport}'); \
	  [ -n "$$t" ] || t=wifi; \
	  v=$$(kubectl get devicestatus $$d -n $(NAMESPACE) \
	       -o jsonpath='{.status.twins[?(@.propertyName=="temperature")].reported.value}' 2>/dev/null); \
	  ts=$$(kubectl get devicestatus $$d -n $(NAMESPACE) \
	       -o jsonpath='{.status.twins[?(@.propertyName=="temperature")].reported.metadata.timestamp}' 2>/dev/null); \
	  age=-; \
	  if [ -n "$$ts" ]; then age=$$(( ( $$(date +%s) - $$ts / 1000 ) ))s; fi; \
	  printf '  %-10s %-14s %-10s %-9s %s\n' "$$d" "$$m" "$$t" "$$age" "$$v"; \
	done; printf '\n'

.PHONY: rbac
controller-gen: ## Regenerate deepcopy after changing any API type
	cd controller && go run sigs.k8s.io/controller-tools/cmd/controller-gen object paths=./api/...

.PHONY: controller-build
plan: ## Dry run — what would happen, without touching a cluster
	cd controller && go run ./cmd/plan --dir ../examples/north-ridge

.PHONY: plan-dir
plan-dir: ## Dry run against your own manifests: make plan-dir DIR=manifests
	cd controller && go run ./cmd/plan --dir ../$(or $(DIR),manifests)

.PHONY: controller-build
controller-build: ## Build and test the controllers
	cd controller && go build ./... && go test ./...

.PHONY: liveness-crd
liveness-crd: ## Install the DeviceLiveness CRD and its RBAC
	kubectl apply -f manifests/deviceliveness-crd.yaml

.PHONY: liveness-run
liveness-run: ## Run the liveness controller locally against the cluster
	cd controller && go run ./cmd/ --leader-elect=false

.PHONY: liveness
serve-fw: ## Serve the built firmware over HTTP for pull-based OTA
	@printf '  serving $(ESPHOME_DIR)/.esphome/build/w10-a/build on :8000\n'
	@printf '  package: http://$(MAC_IP):8000/w10-a.ota.bin\n\n'
	cd $(ESPHOME_DIR)/.esphome/build/w10-a/build && python3 -m http.server 8000

.PHONY: rollout-demo
rollout-demo: ## Apply the bench rollout and watch it
	kubectl apply -f manifests/rollout-bench.yaml
	kubectl get firmwarerollout bench-v43 -n $(NAMESPACE) -w

.PHONY: rollouts
rollouts: ## Rollouts, with the three columns nothing else has
	@kubectl get firmwarerollout -n $(NAMESPACE) 2>/dev/null || \
	  printf '  $(WARN) no FirmwareRollout objects\n'

.PHONY: refused
capability: ## What each device can do, per transport
	@printf '\n  %-10s %-9s %-14s %-7s %-6s %s\n' DEVICE TRANSPORT AVAILABILITY CONFIG OTA REACHABLE-NOW
	@printf '  %-10s %-9s %-14s %-7s %-6s %s\n' ---------- --------- -------------- ------- ------ --------------
	@kubectl get liveness -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  .items[] | .metadata.name as $$d
	  | (.status.capability.reachableVia // []) as $$now
	  | (.status.capability.transports // [])[]
	  | "  \($$d)\t\(.type)\t\(.availability // "-")\t\(.config)\t\(.ota)\t\($$now | join(","))"' \
	  | awk -F'\t' '{printf "  %-10s %-9s %-14s %-7s %-6s %s\n", $$1, $$2, $$3, $$4, $$5, $$6}' \
	  || printf '  $(WARN) no capability recorded — is the liveness controller running?\n'
	@printf '\n'

.PHONY: refused
decommissioned: ## Devices that have left the fleet, and why
	@kubectl get devicedecommission -n $(NAMESPACE) 2>/dev/null || \
	  printf '  $(WARN) no DeviceDecommission objects\n'

.PHONY: attrition
attrition: ## Why devices leave — a procurement question, not an ops one
	@kubectl get devicedecommission -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  [.items[] | select(.status.phase == "Complete")]
	  | group_by(.spec.reason)[]
	  | "\(.[0].spec.reason)\t\(length)"' \
	  | awk -F'\t' 'BEGIN{printf "\n  %-18s %s\n","REASON","COUNT"} {printf "  %-18s %s\n",$$1,$$2} END{print ""}' \
	  || printf '  $(OK) nothing decommissioned\n'

.PHONY: templates
templates: ## Device templates and what they generated
	@kubectl get devicetemplate -n $(NAMESPACE) 2>/dev/null || \
	  printf '  $(WARN) no DeviceTemplate objects\n'

.PHONY: template-drift
template-drift: ## Generated devices edited outside their template
	@kubectl get devicetemplate -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  .items[] | .metadata.name as $$t | (.status.drifted // [])[]
	  | "\($$t)\t\(.)"' \
	  | awk -F'\t' 'BEGIN{printf "\n  %-22s %s\n","TEMPLATE","DEVICE"} {printf "  %-22s %s\n",$$1,$$2} END{print ""}' \
	  || printf '  $(OK) no template drift\n'

.PHONY: queries
queries: ## Fleet queries and what they matched
	@kubectl get fleetquery -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  .items[] | "\(.metadata.name)\t\(.status.matched // 0)\t\(.status.summary.actionable // 0)\t\((.status.matched // 0) - (.status.summary.actionable // 0))"' \
	  | awk -F'\t' 'BEGIN{printf "\n  %-24s %-9s %-12s %s\n","QUERY","MATCHED","ACTIONABLE","NEEDS-SOMETHING-ELSE"} {printf "  %-24s %-9s %-12s %s\n",$$1,$$2,$$3,$$4} END{print ""}' \
	  || printf '  $(WARN) no FleetQuery objects\n'

.PHONY: blocked
blocked: ## Why any rollout is not proceeding, and until when
	@kubectl get firmwarerollout -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  .items[] | select(.status.phase == "Blocked" or .status.phase == "Waiting")
	  | "\(.metadata.name)\t\(.status.phase)\t\(.status.blockedBy // "-")\t\(.status.blockReason // "-")\t\(.status.nextWindow // "-")"' \
	  | awk -F'\t' 'BEGIN{printf "\n  %-14s %-8s %-22s %-26s %s\n","ROLLOUT","PHASE","BLOCKED-BY","REASON","NEXT-WINDOW"} {printf "  %-14s %-8s %-22s %-26s %s\n",$$1,$$2,$$3,$$4,$$5} END{print ""}' \
	  || printf '  $(WARN) nothing blocked\n'

.PHONY: windows
windows: ## Maintenance windows — open, and if not, when
	@kubectl get maintenancewindow -n $(NAMESPACE) 2>/dev/null || \
	  printf '  $(WARN) no MaintenanceWindow objects\n'

.PHONY: contact
contact: ## When each device tends to be reachable, learned not declared
	@kubectl get liveness -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  .items[] | .metadata.name as $$d
	  | (.status.contactWindows // [])[]
	  | "\($$d)\t\(.transport)\t\(.typicalInterval // "-")\t\(.confidence)\t\(.samplesObserved)"' \
	  | awk -F'\t' 'BEGIN{printf "\n  %-12s %-9s %-10s %-10s %s\n", "DEVICE","TRANSPORT","INTERVAL","CONFIDENCE","SAMPLES"} {printf "  %-12s %-9s %-10s %-10s %s\n",$$1,$$2,$$3,$$4,$$5} END{print ""}' \
	  || printf '  $(WARN) nothing learned yet\n'

.PHONY: drift
drift: ## Is the fleet still running what it should be?
	@kubectl get fleetdrift -n $(NAMESPACE) 2>/dev/null || \
	  printf '  $(WARN) no FleetDrift objects\n'

.PHONY: expiry
expiry: ## Who goes dark, when, and who needs a truck
	@kubectl get credentialexpiry -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  .items[] | (.status.devices // [])[]
	  | select(.state != "Healthy")
	  | "\(.device)\t\(.state)\t\(.timeLeft)\t\(.actionRequired)"' \
	  | awk -F'\t' 'BEGIN{printf "\n  %-12s %-9s %-9s %s\n  %-12s %-9s %-9s %s\n", "DEVICE","STATE","LEFT","ACTION","------------","---------","---------","------"} {printf "  %-12s %-9s %-9s %s\n", $$1,$$2,$$3,$$4} END{print ""}' \
	  || printf '  $(WARN) nothing to report\n'

.PHONY: refused
refused: ## Why each device was refused or is pending — the planning result
	@kubectl get firmwarerollout -n $(NAMESPACE) -o json 2>/dev/null | jq -r '
	  .items[] | .metadata.name as $$r
	  | ((.status.refused // [])[] | "\($$r)  REFUSED  \(.device)  \(.reason)  \(.detail)"),
	    ((.status.pending // [])[] | "\($$r)  PENDING  \(.device)  \(.reason)  \(.detail)")' \
	  | column -t -s'  ' || printf '  $(WARN) nothing to report\n'

.PHONY: serve-fw rollout-demo rollouts capability decommissioned attrition templates template-drift queries blocked windows contact drift expiry refused liveness
liveness: ## The row Kubernetes cannot produce
	@kubectl get liveness -n $(NAMESPACE) 2>/dev/null || \
	  printf '  $(WARN) no DeviceLiveness objects — is the controller running?\n'

.PHONY: controller-gen plan plan-dir controller-build liveness-crd rbac
rbac: ## Grant cloudcore access to the DeviceStatus CRD (1.23.1 chart omits it)
	kubectl apply -f manifests/cloudcore-devicestatus-rbac.yaml
	kubectl -n kubeedge rollout restart deploy/cloudcore
	kubectl -n kubeedge rollout status deploy/cloudcore

.PHONY: set
tone: ## Play a tone on the board via kubectl — the V1 demo
	@n=$$(date +%s); $(MAKE) --no-print-directory set PROPERTY=tone_trigger VALUE=$$n; \
	  printf '  patched tone_trigger=%s on $(DEVICE) — listen\n' "$$n"

.PHONY: tone-all
tone-all: ## Play a tone on every device in the fleet, one after another
	@for d in $$(kubectl get devices -n $(NAMESPACE) -o jsonpath='{.items[*].metadata.name}'); do \
	  $(MAKE) --no-print-directory tone DEVICE=$$d; sleep 6; \
	done

.PHONY: v1
v1: ## Watch desired and reported converge on amp_enable
	@printf '  patching amp_enable=ON...\n'
	@$(MAKE) --no-print-directory set PROPERTY=amp_enable VALUE=ON >/dev/null
	@printf '\n  NAME    WANT   GOT\n'
	@for i in 1 2 3 4 5 6 7 8 9 10 11 12; do \
	  w=$$(kubectl get device $(DEVICE) -n $(NAMESPACE) \
	       -o jsonpath='{.spec.properties[?(@.name=="amp_enable")].desired.value}'); \
	  g=$$(kubectl get devicestatus $(DEVICE) -n $(NAMESPACE) \
	       -o jsonpath='{.status.twins[?(@.propertyName=="amp_enable")].reported.value}'); \
	  printf '  $(DEVICE)   %-6s %-6s\n' "$$w" "$$g"; \
	  [ "$$w" = "$$g" ] && { printf '\n  $(OK) converged.\n\n'; exit 0; }; \
	  sleep 3; \
	done; \
	printf '\n  $(BAD) did not converge in 36s\n'; exit 1

.PHONY: tone v1 set
set: ## Patch desired state — make set PROPERTY=amp_enable VALUE=ON
	@[ -n "$(VALUE)" ] || { printf '$(BAD) VALUE= required\n'; exit 1; }
	@# A merge patch on spec.properties REPLACES the whole list — it does not
	@# merge by name. That silently drops every other property AND the visitor
	@# config of the one being set, and the mapper then panics on the nil
	@# visitor. Locate the index and use a JSON patch against that element.
	@i=$$(kubectl get device $(DEVICE) -n $(NAMESPACE) -o json \
	      | jq -r '.spec.properties | to_entries[] | select(.value.name=="$(PROPERTY)") | .key'); \
	  if [ -z "$$i" ]; then \
	    printf '$(BAD) no property named "$(PROPERTY)" on $(DEVICE)\n'; exit 1; fi; \
	  kubectl patch device $(DEVICE) -n $(NAMESPACE) --type=json -p \
	    "[{\"op\":\"replace\",\"path\":\"/spec/properties/$$i/desired/value\",\"value\":\"$(VALUE)\"}]"

# ===========================================================================
##@ Mapper
# ===========================================================================

.PHONY: mapper-build
guard-paths: ## Verify configured paths exist before anything uses them
	@ok=1; \
	for p in "$(MAPPER_DIR)" "$(ESPHOME_DIR)"; do \
	  [ -d "$$p" ] || { printf '  $(BAD) missing: %s\n' "$$p"; ok=0; }; \
	done; \
	[ -f "$(ESPHOME_DIR)/secrets.yaml" ] || \
	  printf '  $(WARN) no $(ESPHOME_DIR)/secrets.yaml — copy secrets.yaml.example\n'; \
	[ "$$ok" = 1 ] || exit 1
	@printf '  $(OK) paths ok\n'

.PHONY: guard-paths mapper-build
mapper-build: guard-paths ## Build the mapper for linux/arm64 and copy it into the VM
	cd $(MAPPER_DIR) && GOOS=linux GOARCH=arm64 go build -o /tmp/$(MAPPER_BIN) ./cmd/
	limactl copy /tmp/$(MAPPER_BIN) $(VM):/tmp/$(MAPPER_BIN)
	limactl shell $(VM) -- sudo install -m 0755 /tmp/$(MAPPER_BIN) /usr/local/bin/$(MAPPER_BIN)

.PHONY: mapper-run
mapper-install: mapper-build ## Install the mapper as a systemd unit in the VM
	limactl copy deploy/esphome-mapper.service $(VM):/tmp/
	limactl shell $(VM) -- sudo install -m0644 /tmp/esphome-mapper.service /etc/systemd/system/
	limactl shell $(VM) -- sudo systemctl daemon-reload
	limactl shell $(VM) -- sudo systemctl enable --now esphome-mapper
	@sleep 3
	limactl shell $(VM) -- systemctl is-active esphome-mapper

.PHONY: mapper-install mapper-run
mapper-run: ## Run the mapper in the foreground (dev loop)
	limactl shell $(VM) -- sudo /usr/local/bin/$(MAPPER_BIN) --v=4

.PHONY: mapper-logs
mapper-logs: ## Follow mapper logs if installed as a unit
	limactl shell $(VM) -- sudo journalctl -u $(MAPPER_BIN) -f

.PHONY: mapper-restart
mapper-restart: ## Restart the mapper unit
	limactl shell $(VM) -- sudo systemctl restart $(MAPPER_BIN)

.PHONY: dev
dev: mapper-build mapper-run ## Rebuild and run — the inner loop

# ===========================================================================
##@ Firmware
# ===========================================================================
# ESPHome's component cache is not concurrency-safe. These targets must never
# run in parallel; .NOTPARALLEL at the top of this file enforces it.

.PHONY: flash
flash: ## Flash the board over the air (CONFIG=w10-msg-a.yaml)
	cd $(ESPHOME_DIR) && esphome run $(ESPHOME_CONFIG)

.PHONY: flash-a
flash-a: ## OTA flash w10-a by IP — no port menu, no mDNS
	cd $(ESPHOME_DIR) && esphome run w10-msg-a.yaml --device $(W10A_IP)

.PHONY: flash-b
flash-b: gen-b ## OTA flash w10-b by IP — regenerates from A first
	cd $(ESPHOME_DIR) && esphome run w10-msg-b.yaml --device $(W10B_IP)

.PHONY: gen-b
gen-b: ## Regenerate w10-msg-b.yaml from A — run after every change to A
	@cd $(ESPHOME_DIR) && sed \
	  -e 's/^  node_name: w10-a/  node_name: w10-b/' \
	  -e 's/^  node_id: "1"/  node_id: "2"/' \
	  -e 's/^  node_label: "A"/  node_label: "B"/' \
	  w10-msg-a.yaml > w10-msg-b.yaml
	@cd $(ESPHOME_DIR) && d=$$(diff w10-msg-a.yaml w10-msg-b.yaml | grep -c '^[<>]'); \
	  if [ "$$d" -ne 6 ]; then \
	    printf '  $(BAD) expected 6 differing lines, got %s\n' "$$d"; exit 1; fi
	@printf '  $(OK) w10-msg-b.yaml regenerated (substitutions only)\n'

.PHONY: fw-logs
fw-logs: ## ESPHome serial/API logs
	cd $(ESPHOME_DIR) && esphome logs $(ESPHOME_CONFIG)

.PHONY: fw-compile
fw-compile: ## Compile only, no upload
	cd $(ESPHOME_DIR) && esphome compile $(ESPHOME_CONFIG)

.PHONY: fw-clean
fw-clean: ## Clear the build cache (fixes corruption from a parallel build)
	cd $(ESPHOME_DIR) && esphome clean $(ESPHOME_CONFIG)

# ===========================================================================
##@ Lifecycle
# ===========================================================================

.PHONY: up
bench: ## Four-pane workbench — both boards on the Mac, mapper and a shell in the VM
	@bash hack/bench.sh

.PHONY: bench-kill
bench-kill: ## Tear the workbench down
	@bash hack/bench.sh kill

.PHONY: clean-fw
clean-fw: ## Clear both build caches — ESPHome will happily flash a stale binary
	cd $(ESPHOME_DIR) && esphome clean w10-msg-a.yaml
	cd $(ESPHOME_DIR) && esphome clean w10-msg-b.yaml

.PHONY: bench up
up: broker-up vm-up cluster-start ## Bring everything up
	@$(MAKE) --no-print-directory check

.PHONY: down
down: cluster-down vm-down ## Bring everything down (broker left running)

.PHONY: restart
restart: down up ## Cycle everything
MOSQUITTO_CONF ?= /opt/homebrew/etc/mosquitto/mosquitto.conf

.PHONY: broker-fg
broker-fg: ## Run mosquitto in the foreground, verbose — connections visible
	/opt/homebrew/opt/mosquitto/sbin/mosquitto -c $(MOSQUITTO_CONF) -v

