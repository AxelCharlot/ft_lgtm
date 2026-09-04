#!/bin/bash
#
# Prepares the guest so that the project can build and run on it.


set -euo pipefail

readonly PACKAGES=(
  ca-certificates
  cloud-guest-utils
  curl
  docker.io
  git
  jq
  make
)

readonly DOCKER_USER='vagrant'

readonly APT_TIMERS=(
  apt-daily.timer
  apt-daily-upgrade.timer
)

readonly HOSTS_FILE='/etc/hosts'
readonly HOSTS_ADDRESS='127.0.0.1'
readonly HOSTS_NAMES=(
  lgtm.local
  grafana.lgtm.local
  ipfs.lgtm.local
)

readonly HOSTS_BEGIN='# BEGIN lgtm'
readonly HOSTS_END='# END lgtm'

readonly K3S_VERSION="${LGTM_K3S_VERSION:-v1.36.3+k3s1}"
readonly K3S_INSTALL_URL='https://get.k3s.io'

readonly KUBECONFIG_PATH='/etc/rancher/k3s/k3s.yaml'
readonly KUBECONFIG_PROFILE='/etc/profile.d/lgtm-kubeconfig.sh'

readonly CLUSTER_TIMEOUT_SECONDS=300

log() {
  echo "[bootstrap] $*"
}

err() {
  echo "[bootstrap] $*" >&2
}

wait_until() {
  local description="$1"
  local timeout_seconds="$2"
  shift 2

  local deadline=$(( SECONDS + timeout_seconds ))
  until "$@" >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      err "gave up on ${description} after ${timeout_seconds} seconds"
      return 1
    fi
    sleep 5
  done

  log "${description}"
}

# The box boots and immediately starts replacing ninety of its own packages,
# libc6, systemd, dpkg and the kernel among them. Two things are wrong with that
# here. It holds the dpkg lock for minutes, so whether this script can install
# anything is a race. And it undoes the pin: the Vagrantfile fixes a box version
# so that a rebuild in six months gives the same guest, and a guest that patches
# itself on every boot is not that guest.
#
remove_unattended_upgrades() {
  if ! dpkg-query --show unattended-upgrades >/dev/null 2>&1; then
    log "unattended-upgrades is already gone"
    return 0
  fi

  log "removing unattended-upgrades"

  systemctl disable --now "${APT_TIMERS[@]}"
  systemctl stop apt-daily.service apt-daily-upgrade.service unattended-upgrades.service
  dpkg --configure --pending

  apt-get purge --yes --quiet unattended-upgrades
}

install_packages() {
  log "installing ${#PACKAGES[@]} packages"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update --quiet
  apt-get install --yes --quiet "${PACKAGES[@]}"
}

add_user_to_docker_group() {
  if id --name --groups "${DOCKER_USER}" | grep --quiet --word-regexp docker; then
    log "${DOCKER_USER} already belongs to the docker group"
    return 0
  fi

  usermod --append --groups docker "${DOCKER_USER}"
  log "added ${DOCKER_USER} to the docker group"
}

grow_root_filesystem() {
  local root_source disk_name partition_suffix partition_number
  local growpart_output growpart_status

  root_source="$(findmnt --noheadings --output SOURCE --target /)"
  disk_name="$(lsblk --noheadings --output PKNAME "${root_source}")"

  if [[ -z "${disk_name}" ]]; then
    log "${root_source} sits on no partition, so nothing is grown"
    return 0
  fi

  partition_suffix="${root_source##*"${disk_name}"}"
  partition_number="${partition_suffix#p}"

  growpart_status=0
  growpart_output="$(growpart "/dev/${disk_name}" "${partition_number}" 2>&1)" ||
    growpart_status=$?

  if (( growpart_status != 0 )); then
    if [[ "${growpart_output}" == *NOCHANGE* ]]; then
      log "the root partition already fills /dev/${disk_name}"
    else
      err "growpart failed on /dev/${disk_name} partition ${partition_number}"
      err "${growpart_output}"
      return 1
    fi
  else
    log "grew partition ${partition_number} of /dev/${disk_name}"
  fi

  resize2fs "${root_source}"
}

write_host_names() {
  local name

  sed --in-place "/^${HOSTS_BEGIN}$/,/^${HOSTS_END}$/d" "${HOSTS_FILE}"

  {
    echo "${HOSTS_BEGIN}"
    for name in "${HOSTS_NAMES[@]}"; do
      echo "${HOSTS_ADDRESS} ${name}"
    done
    echo "${HOSTS_END}"
  } >>"${HOSTS_FILE}"

  log "wrote ${#HOSTS_NAMES[@]} names into ${HOSTS_FILE}"
}

install_k3s() {
  if systemctl is-active --quiet k3s; then
    log "k3s ${K3S_VERSION} is already running"
    return 0
  fi

  log "installing k3s ${K3S_VERSION}"
  curl --silent --show-error --fail --location "${K3S_INSTALL_URL}" |
    INSTALL_K3S_VERSION="${K3S_VERSION}" sh -s - --write-kubeconfig-mode 644
}

write_kubeconfig_profile() {
  cat >"${KUBECONFIG_PROFILE}" <<PROFILE
# Written by vm/bootstrap.sh. Edits are lost on the next provision.
export KUBECONFIG=${KUBECONFIG_PATH}
PROFILE

  log "KUBECONFIG points at ${KUBECONFIG_PATH} for every login shell"
}

local_path_is_default() {
  local is_default
  is_default="$(k3s kubectl get storageclass local-path \
    -o 'jsonpath={.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}' \
    2>/dev/null)"

  [[ "${is_default}" == 'true' ]]
}

check_cluster_answers() {
  wait_until 'the node is Ready' "${CLUSTER_TIMEOUT_SECONDS}" \
    k3s kubectl wait --for=condition=Ready node --all --timeout=10s

  wait_until 'local-path is the default storage class' 60 local_path_is_default

  wait_until 'Traefik is rolled out' "${CLUSTER_TIMEOUT_SECONDS}" \
    k3s kubectl --namespace kube-system rollout status deployment/traefik --timeout=10s

  wait_until 'Traefik answers on port 80' "${CLUSTER_TIMEOUT_SECONDS}" \
    curl --silent --output /dev/null --max-time 5 http://127.0.0.1/
}

main() {
  if (( EUID != 0 )); then
    err "this script needs root, and Vagrant already gives it that"
    return 1
  fi

  remove_unattended_upgrades
  install_packages
  add_user_to_docker_group
  grow_root_filesystem
  write_host_names
  install_k3s
  write_kubeconfig_profile
  check_cluster_answers

  log "the machine is ready"
}

main "$@"
