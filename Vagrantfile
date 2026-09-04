# -*- mode: ruby -*-
# vi: set ft=ruby :

# One machine, one provider: libvirt.
#
# Nothing above this file is provider specific. K3s, the manifests and the Go

VM_MEMORY = Integer(ENV.fetch('LGTM_VM_MEMORY', '8192'))
VM_CPUS = Integer(ENV.fetch('LGTM_VM_CPUS', '3'))
VM_DISK_GB = Integer(ENV.fetch('LGTM_VM_DISK_GB', '30'))

BOX_NAME = 'debian/bookworm64'
BOX_VERSION = '12.20250126.1'
PRIVATE_IP = '192.168.56.10'

# A bridged interface is what makes the IPFS node dialable from the internet,
# which is what lets a public gateway serve our CID.
BRIDGE_INTERFACE = ENV.fetch('LGTM_BRIDGE', '').strip

# libvirt writes the guest disk into a storage pool, and its `default` pool
# lives under /var. A 4 GB /var is common, and this guest measured 9.6 GB after
# a full build.
POOL_OVERRIDE = ENV.fetch('LGTM_LIBVIRT_POOL', '').strip
POOL_ROOM_GB = 16

# Every pool libvirt knows, with the bytes it says are left in each.
def libvirt_pools
  listing = `virsh --connect qemu:///system pool-list --name 2>/dev/null`
  return nil unless $?.success?

  listing.each_line.map do |line|
    name = line.strip
    next unless name.match?(/\A[\w.-]+\z/)

    info = `virsh --connect qemu:///system pool-info #{name} --bytes 2>/dev/null`
    free = $?.success? ? info[/^Available:\s+(\d+)/, 1] : nil
    free.nil? ? nil : [name, free.to_i]
  end.compact
rescue Errno::ENOENT
  nil
end

# True once the domain exists. The pool only matters while the machine is being
# created; after that the disk already sits somewhere.
def machine_created?
  File.exist?(File.expand_path('.vagrant/machines/default/libvirt/id', File.dirname(__FILE__)))
end

def no_pool_message(pools)
  found = pools.map { |name, free| format('%s (%.0f GB)', name, free / 1024.0**3) }.join(', ')
  <<~MESSAGE
    lgtm: no libvirt storage pool has room for the guest.

    Needed: #{POOL_ROOM_GB} GB free. Found: #{found.empty? ? 'no pool at all' : found}.

    Free some room, or make a pool on a partition that has it:

      virsh --connect qemu:///system pool-define-as lgtm dir - - - - /path/with/room
      virsh --connect qemu:///system pool-build lgtm
      virsh --connect qemu:///system pool-start lgtm
      virsh --connect qemu:///system pool-autostart lgtm
  MESSAGE
end

# The pool the guest disk goes into.
def libvirt_pool
  return POOL_OVERRIDE unless POOL_OVERRIDE.empty?
  return 'default' if machine_created?

  pools = libvirt_pools
  return 'default' if pools.nil?

  fitting = pools.select { |_, free| free >= POOL_ROOM_GB * 1024**3 }
  chosen = fitting.assoc('default') || fitting.max_by { |_, free| free }

  if chosen.nil?
    abort(no_pool_message(pools)) if ARGV.first == 'up'
    warn(no_pool_message(pools))
    return 'default'
  end

  warn(format('==> lgtm: the guest disk goes into the libvirt pool %s, %.0f GB free',
              chosen[0], chosen[1] / 1024.0**3))
  chosen[0]
end

LIBVIRT_POOL = libvirt_pool

Vagrant.configure('2') do |config|
  config.vm.box = BOX_NAME
  config.vm.box_version = BOX_VERSION
  config.vm.hostname = 'lgtm'
  config.vm.network 'private_network', ip: PRIVATE_IP

  # IPFS swarm traffic, when a bridge is named.
  unless BRIDGE_INTERFACE.empty?
    config.vm.network 'public_network',
                      dev: BRIDGE_INTERFACE,
                      mode: 'bridge',
                      type: 'bridge'
  end

  # rsync, and not a shared filesystem: the Docker build context has to sit on a
  # real filesystem, with real ownership and working file watching.
  config.vm.synced_folder '.', '/vagrant',
                          type: 'rsync',
                          rsync__exclude: ['.git/', '.vagrant/', 'en.subject.pdf']

  # Vagrant uploads this script and runs it as root on the first boot;
  # `vagrant provision` runs it again, and it is written to survive that.
  config.vm.provision 'shell', path: 'vm/bootstrap.sh'

  config.vm.provider :libvirt do |libvirt|
    libvirt.memory = VM_MEMORY
    libvirt.cpus = VM_CPUS
    libvirt.driver = 'kvm'
    libvirt.storage_pool_name = LIBVIRT_POOL
    libvirt.machine_virtual_size = VM_DISK_GB
  end

  config.vm.post_up_message = <<~MESSAGE
    The machine is up.

    Add these three lines to the hosts file of **this** machine, not of the
    guest, then open http://lgtm.local in a browser:

      #{PRIVATE_IP} lgtm.local
      #{PRIVATE_IP} grafana.lgtm.local
      #{PRIVATE_IP} ipfs.lgtm.local

    `make hosts-client` prints them again whenever you need them.
  MESSAGE
end
