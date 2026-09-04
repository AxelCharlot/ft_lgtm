# Builds and runs the station on a machine that has nothing on it.

SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

.NOTPARALLEL:

VAGRANT ?= vagrant
GUEST := $(VAGRANT) ssh --command

NAMESPACE := lgtm
KUBECTL := kubectl --namespace $(NAMESPACE)
IMAGE_TAG := v1
IMAGES := backend frontend
DEV_DEPLOYMENTS := lgtm-backend lgtm-frontend
ROLLOUT_TIMEOUT := 300s
PRIVATE_IP := $(shell sed -n "s/^PRIVATE_IP = '\(.*\)'/\1/p" Vagrantfile)
HOST_NAMES := $(shell sed -n '/^readonly HOSTS_NAMES=(/,/^)/{//!p}' vm/bootstrap.sh)
COMPONENT ?= lgtm-backend
LOG_LINES ?= 100

.PHONY: all up install sync build dev hosts hosts-client clean fclean

all: up

up:
	$(VAGRANT) up
	$(MAKE) dev

install:
	$(VAGRANT) up --provision

sync:
	$(VAGRANT) rsync

build: sync
	for name in $(IMAGES); do \
		$(GUEST) "docker build --tag lgtm/$$name:$(IMAGE_TAG) /vagrant/$$name"; \
		$(GUEST) "set -o pipefail; docker save lgtm/$$name:$(IMAGE_TAG) | sudo k3s ctr images import -"; \
	done
	$(GUEST) "docker image prune --force"

dev: build
	for name in $(DEV_DEPLOYMENTS); do \
		$(GUEST) "if $(KUBECTL) get deployment/$$name >/dev/null 2>&1; then \
			$(KUBECTL) rollout restart deployment/$$name; \
			$(KUBECTL) rollout status deployment/$$name --timeout=$(ROLLOUT_TIMEOUT); \
		else \
			echo 'deployment/$$name is not deployed yet, so nothing to restart'; \
		fi"; \
	done

hosts:
	$(GUEST) "sed --quiet '/# BEGIN lgtm/,/# END lgtm/p' /etc/hosts"

hosts-client:
	@echo 'Add these three lines to the hosts file of THIS machine, not of the guest:'
	@echo
	@$(foreach name,$(HOST_NAMES),echo '  $(PRIVATE_IP) $(name)';)
	@echo
	@echo 'That file is:'
	@echo '  Linux, macOS   /etc/hosts                             needs sudo'
	@printf '  Windows        C:\\Windows\\System32\\drivers\\etc\\hosts  as Administrator\n'
	@echo
	@echo 'The address is the private network of the guest, fixed in the Vagrantfile.'
	@echo 'It is not 127.0.0.1: nothing forwards port 80 from this machine, and'
	@echo 'nothing needs to. Traefik binds port 80 inside the guest, where the port'
	@echo 'costs no privilege, so no part of this project ever needs root here.'
	@echo
	@echo 'Then open, with no port:'
	@$(foreach name,$(HOST_NAMES),echo '  http://$(name)';)

clean:
	$(GUEST) "kubectl delete namespace $(NAMESPACE) --ignore-not-found --wait"

fclean: clean
	$(GUEST) "sudo /usr/local/bin/k3s-uninstall.sh"
