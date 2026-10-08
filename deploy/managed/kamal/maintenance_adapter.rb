# Explicitly loaded only by the host-local managed-release controller.
# Ordinary Kamal deployments continue to use probe_host.rb.
require_relative "probe_host"
require "json"

module LeapViewManagedMaintenance
  IMAGE = /\A[a-z0-9][a-z0-9._:\/-]*@sha256:[a-f0-9]{64}\z/
  APP_OPTIONS = %w[read-only cap-drop security-opt pids-limit memory tmpfs cpus restart].freeze

  def self.image(key)
    value = ENV.fetch(key)
    raise ArgumentError, "managed image must be immutable" unless IMAGE.match?(value)
    value
  end

  def self.publish?
    path = ENV.fetch("LEAPVIEW_MANAGED_GATE")
    raise ArgumentError, "managed ingress gate must be absolute" unless path.start_with?("/")
    stat = File.lstat(path)
    raise ArgumentError, "managed ingress gate must be private and regular" unless stat.file? && (stat.mode & 0o077).zero? && stat.size <= 1024
    gate = JSON.parse(File.read(path))
    raise ArgumentError, "invalid managed ingress gate" unless gate.keys == ["publish"] && [true, false].include?(gate["publish"])
    gate.fetch("publish")
  end

  def self.validate!(config)
    raise ArgumentError, "managed adapter requires Kamal 2.12.0" unless Gem.loaded_specs.fetch("kamal").version.to_s == "2.12.0"
    raise ArgumentError, "managed revision must be a full commit" unless /\A[a-f0-9]{40}\z/.match?(config.version)
    image("LEAPVIEW_MANAGED_IMAGE")
    image("LEAPVIEW_MANAGED_PROXY_IMAGE")
    raise ArgumentError, "managed profile requires one local web role" unless config.roles.map(&:name) == ["web"] && config.role("web").hosts == ["127.0.0.1"]
    raise ArgumentError, "managed profile cannot have accessories or a destination" unless config.accessories.empty? && config.destination.nil?
    role = config.role("web")
    options = config.raw_config.servers.fetch("web").fetch("options", {})
    raise ArgumentError, "unsupported managed application Docker option" unless (options.keys - APP_OPTIONS).empty?
    raise ArgumentError, "managed profile cannot override its command, labels, or proxy" if %w[cmd labels proxy asset_path].any? { |key| config.raw_config.servers.fetch("web").key?(key) }
    raise ArgumentError, "managed profile requires the dedicated proxy" unless role.running_proxy? && config.proxy.hosts.length == 1 && config.proxy.app_port == 8080 && config.proxy.run&.version == "v0.9.2"
    raise ArgumentError, "managed profile cannot customize proxy Docker options" if config.proxy.run.run_config.fetch("options", {}).any?
    raise ArgumentError, "managed profile cannot customize proxy ports or bindings" if %w[http_port https_port bind_ips metrics_port publish].any? { |key| config.proxy.run.run_config.key?(key) }
    raise ArgumentError, "managed profile cannot configure assets, error pages, or labels" if config.asset_path || config.error_pages_path || config.labels.present?
    raise ArgumentError, "managed profile cannot install hooks" unless Dir.glob(File.join(config.hooks_path, "**", "*"), File::FNM_DOTMATCH).reject { |path| File.directory?(path) }.empty?
    ssh = config.raw_config.ssh
    raise ArgumentError, "managed profile requires pinned root SSH" unless ssh && ssh["user"] == "root" && ssh["config"] == ["ssh_config"] && ssh["forward_agent"] == false && (ssh.keys - %w[user config forward_agent]).empty?
    validate_ssh!
  end

  def self.validate_ssh!
    lines = File.readlines("ssh_config").map(&:strip).reject { |line| line.empty? || line.start_with?("#") }
    required = ["Host 127.0.0.1", "StrictHostKeyChecking yes", "ForwardAgent no"]
    raise ArgumentError, "managed SSH must pin the local host" unless (required - lines).empty?
    raise ArgumentError, "managed SSH must use a known-hosts file" unless lines.count { |line| /\AUserKnownHostsFile (?:\/|~\/)[^\s]+\z/.match?(line) } == 1
    raise ArgumentError, "unsupported managed SSH directive" unless lines.all? { |line| required.include?(line) || /\A(?:UserKnownHostsFile|IdentityFile) (?:\/|~\/)[^\s]+\z/.match?(line) }
  end

  def self.require_controller_lock!
    descriptor = Integer(ENV.fetch("LEAPVIEW_MANAGED_LOCK_FD"), 10)
    path = ENV.fetch("LEAPVIEW_MANAGED_LOCK_PATH")
    raise ArgumentError, "managed mutation requires a controller lock" unless descriptor >= 3 && path.start_with?("/")
    # The controller passes the same flock open-file-description through exec.
    # Keep it open for this entire CLI process; a boolean environment assertion
    # alone would not establish exclusion, and Kamal's mkdir lock survives kills.
    file = File.for_fd(descriptor, autoclose: false)
    actual, expected = file.stat, File.lstat(path)
    unless actual.file? && expected.file? && actual.ino == expected.ino && actual.dev == expected.dev && actual.uid == Process.euid && (actual.mode & 0o077).zero?
      raise ArgumentError, "managed controller lock identity mismatch"
    end
    raise ArgumentError, "managed controller lock is held by another owner" unless file.flock(File::LOCK_EX | File::LOCK_NB)
    true
  end

  def self.with_controller_deadline
    deadline = Integer(ENV.fetch("LEAPVIEW_MANAGED_DEADLINE_UNIX_MS"), 10)
    remaining = deadline.fdiv(1000) - Time.now.to_f
    raise ArgumentError, "managed mutation requires a live bounded deadline" unless remaining.positive? && remaining <= 24 * 60 * 60
    raise ArgumentError, "managed mutation requires an isolated process group" unless Process.getpgrp == Process.pid
    # Go also cancels this process group. This independent watchdog remains when
    # the controller itself is killed, so a stalled local SSH client cannot hold
    # its inherited flock indefinitely. Remote commands still require the app's
    # startup admission and home lock; killing this group is not a remote kill.
    watchdog = Thread.new do
      sleep remaining
      Process.kill("KILL", -Process.pid)
    end
    begin
      yield
    ensure
      watchdog.kill
      watchdog.join
    end
  end

  module Configuration
    def initialize(...)
      super
      LeapViewManagedMaintenance.validate!(self)
    end

    def absolute_image
      LeapViewManagedMaintenance.image("LEAPVIEW_MANAGED_IMAGE")
    end
  end

  module ProxyRun
    def publish?
      LeapViewManagedMaintenance.publish?
    end

    def image
      LeapViewManagedMaintenance.image("LEAPVIEW_MANAGED_PROXY_IMAGE")
    end

    def options_args
      [*super, "--pull=never"]
    end
  end

  module Role
    def option_args
      [*super, "--pull=never"]
    end
  end

  module Proxy
    def deploy_options
      super.merge("health-check-path": "/maintenance/readyz")
    end
  end

  module Registry
    def login(registry_config: nil)
      docker :image, :inspect, "--format", "'{{.Id}}'",
        LeapViewManagedMaintenance.image("LEAPVIEW_MANAGED_IMAGE"),
        LeapViewManagedMaintenance.image("LEAPVIEW_MANAGED_PROXY_IMAGE")
    end
  end

  module LocalDocker
    private

    def docker(*args)
      # Loopback SSH must address the same daemon as the controller inventory,
      # regardless of the root account's selected Docker context or environment.
      super("--host", "unix:///var/run/docker.sock", *args)
    end
  end

  module ProxyCommands
    def remove_container
      # Stock Kamal prunes every stopped container carrying the proxy label.
      # Restrict recovery to our exact proxy name and retain lookup failures.
      lookup = docker(:container, :ls, "--all", "--filter", "'name=^/kamal-proxy$'", "--quiet").join(" ")
      remove = docker(:container, :rm, '"$proxy_id"').join(" ")
      shell([%(proxy_id=$(#{lookup}) && if [ -n "$proxy_id" ]; then #{remove}; fi)])
    end
  end

  module ControllerLock
    private

    def modify(lock: false, &block)
      LeapViewManagedMaintenance.require_controller_lock!
      LeapViewManagedMaintenance.with_controller_deadline { super(lock: false, &block) }
    end
  end
end

Kamal::Configuration.prepend(LeapViewManagedMaintenance::Configuration)
Kamal::Configuration::Proxy::Run.prepend(LeapViewManagedMaintenance::ProxyRun)
Kamal::Configuration::Role.prepend(LeapViewManagedMaintenance::Role)
Kamal::Configuration::Proxy.prepend(LeapViewManagedMaintenance::Proxy)
Kamal::Commands::Base.prepend(LeapViewManagedMaintenance::LocalDocker)
Kamal::Commands::Registry.prepend(LeapViewManagedMaintenance::Registry)
Kamal::Commands::Proxy.prepend(LeapViewManagedMaintenance::ProxyCommands)
Kamal::Cli::Base.prepend(LeapViewManagedMaintenance::ControllerLock)
