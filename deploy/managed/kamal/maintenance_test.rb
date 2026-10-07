require "bundler/setup"
require_relative "maintenance_adapter"
require "minitest/autorun"
require "pathname"
require "tmpdir"
require "fileutils"
require "open3"
require "rbconfig"

class ManagedKamalMaintenanceTest < Minitest::Test
  def setup
    @old_env = ENV.to_h
    @directory = Dir.mktmpdir("leapview-kamal-maintenance")
    @old_directory = Dir.pwd
    Dir.chdir(@directory)
    FileUtils.cp(File.join(__dir__, "maintenance_ssh_config.example"), "ssh_config")
    @raw = File.read(File.join(__dir__, "maintenance_deploy.yml.example"))
    File.write("deploy.yml", @raw)
    File.write("gate.json", JSON.generate(publish: false), perm: 0o600)
    ENV.update(
      "KAMAL_APP_HOSTNAME" => "dash.example.com",
      "KAMAL_REGISTRY_USERNAME" => "fixture",
      "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID" => "fixture-pool",
      "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST" => "fixture-digest",
      "LEAPVIEW_MANAGED_IMAGE" => "ghcr.io/flidai/leapview@sha256:#{'a' * 64}",
      "LEAPVIEW_MANAGED_PROXY_IMAGE" => "basecamp/kamal-proxy@sha256:#{'b' * 64}",
      "LEAPVIEW_MANAGED_GATE" => File.join(@directory, "gate.json"),
      "LEAPVIEW_MANAGED_REVISION" => "c" * 40,
      "BUNDLE_GEMFILE" => File.join(__dir__, "Gemfile")
    )
    FileUtils.mkdir_p(".kamal")
    # Distinct values ensure the projection resolves the private secret file.
    keys = %w[KAMAL_REGISTRY_PASSWORD LEAPVIEW_AGENT_CREDENTIAL_KEY LEAPVIEW_CSRF_KEY LEAPVIEW_METRICS_BEARER_TOKEN LEAPVIEW_POSTGRES_CONTROL_URL LEAPVIEW_POSTGRES_DUCKLAKE_URL LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL]
    File.write(".kamal/secrets", keys.map { |key| "#{key}=fixture-#{key}" }.join("\n"), perm: 0o600)
  end

  def teardown
    Dir.chdir(@old_directory)
    FileUtils.remove_entry(@directory)
    ENV.replace(@old_env)
  end

  def config
    Kamal::Configuration.create_from(config_file: Pathname.new("deploy.yml"), version: ENV.fetch("LEAPVIEW_MANAGED_REVISION"))
  end

  def rewrite(from, to)
    File.write("deploy.yml", @raw.sub(from, to))
  end

  def test_exact_local_images_and_private_proxy_boot
    subject = config
    assert_equal ENV.fetch("LEAPVIEW_MANAGED_IMAGE"), subject.absolute_image
    assert_equal ENV.fetch("LEAPVIEW_MANAGED_PROXY_IMAGE"), subject.proxy.run.image
    assert_nil subject.proxy.run.publish_args
    assert_includes subject.proxy.run.options_args, "--pull=never"
    assert_includes subject.role("web").option_args, "--pull=never"
    command = Kamal::Commands::Registry.new(subject).login.join(" ")
    assert_includes command, "docker image inspect"
    assert_includes command, ENV.fetch("LEAPVIEW_MANAGED_IMAGE")
    assert_includes command, ENV.fetch("LEAPVIEW_MANAGED_PROXY_IMAGE")
    refute_includes command, "login"
    refute_includes command, "fixture-KAMAL_REGISTRY_PASSWORD"
    app_command = Kamal::Commands::App.new(subject, role: subject.role("web"), host: "127.0.0.1").run.join(" ")
    assert_includes app_command, "--pull=never"
    assert_includes app_command, ENV.fetch("LEAPVIEW_MANAGED_IMAGE")
    assert_includes app_command, "--name leapview-web-#{'c' * 40}"
    proxy_command = Kamal::Commands::Proxy.new(subject, host: "127.0.0.1").run.join(" ")
    assert_includes proxy_command, "--pull=never"
    assert_includes proxy_command, ENV.fetch("LEAPVIEW_MANAGED_PROXY_IMAGE")
    refute_includes proxy_command, "--publish"
  end

  def test_prepared_probe_preserves_public_host
    options = config.role("web").proxy.deploy_options
    assert_equal "/maintenance/readyz", options.fetch(:"health-check-path")
    assert_equal "dash.example.com", options.fetch(:"health-check-host")
  end

  def test_publication_requires_explicit_durable_true
    subject = config
    File.write("gate.json", JSON.generate(publish: true))
    assert_includes subject.proxy.run.publish_args, "80:80"
    File.write("gate.json", JSON.generate(publish: "true"))
    assert_raises(ArgumentError) { subject.proxy.run.publish_args }
    File.unlink("gate.json")
    assert_raises(Errno::ENOENT) { subject.proxy.run.publish_args }
  end

  def test_gate_cannot_be_public_or_a_symlink
    subject = config
    File.chmod(0o644, "gate.json")
    assert_raises(ArgumentError) { subject.proxy.run.publish_args }
    File.rename("gate.json", "gate-target.json")
    File.chmod(0o600, "gate-target.json")
    File.symlink("gate-target.json", "gate.json")
    assert_raises(ArgumentError) { subject.proxy.run.publish_args }
  end

  def test_rejects_mutable_images
    ENV["LEAPVIEW_MANAGED_IMAGE"] = "ghcr.io/flidai/leapview:latest"
    assert_raises(ArgumentError) { config }
  end

  def test_rejects_remote_controller_target
    rewrite("- 127.0.0.1", "- other.example.com")
    assert_raises(ArgumentError) { config }
  end

  def test_rejects_extra_roles_and_accessories
    rewrite("servers:\n", "servers:\n  workers:\n    hosts:\n      - 127.0.0.1\n")
    assert_raises(ArgumentError) { config }
    File.write("deploy.yml", @raw + "\naccessories:\n  other:\n    image: postgres:17\n    host: 127.0.0.1\n")
    assert_raises(ArgumentError) { config }
  end

  def test_rejects_alternate_ingress_and_network
    %w[publish network entrypoint volume].each do |option|
      rewrite("    options:", "    options:\n      #{option}: unsafe")
      assert_raises(ArgumentError, option) { config }
    end
  end

  def test_rejects_proxy_option_bypasses
    rewrite("    version: v0.9.2", "    options:\n      publish: 8080:8080\n    version: v0.9.2")
    assert_raises(ArgumentError) { config }
  end

  def test_rejects_hooks
    FileUtils.mkdir_p(".kamal/hooks")
    File.write(".kamal/hooks/pre-app-boot", "exit 0")
    assert_raises(ArgumentError) { config }
  end

  def test_rejects_unpinned_or_redirected_ssh
    original = File.read("ssh_config")
    [original.sub("StrictHostKeyChecking yes", "StrictHostKeyChecking no"), original + "\n  HostName external.example.com\n"].each do |bad|
      File.write("ssh_config", bad)
      assert_raises(ArgumentError) { config }
    end
  end

  def test_projection_resolves_credentials_without_registry_access
    output, error, status = Open3.capture3(RbConfig.ruby, "-W0", File.join(__dir__, "maintenance_config.rb"))
    assert status.success?, error
    projection = JSON.parse(output)
    assert_equal ["127.0.0.1"], projection.fetch("hosts")
    assert_equal ["web"], projection.fetch("roles")
    assert_equal "fixture-LEAPVIEW_CSRF_KEY", projection.fetch("environment").fetch("LEAPVIEW_CSRF_KEY")
    assert_equal "/var/lib/leapview/home/maintenance.sock", projection.fetch("environment").fetch("LEAPVIEW_MAINTENANCE_SOCKET")
    assert_equal ENV.fetch("LEAPVIEW_MANAGED_IMAGE"), projection.fetch("image")
  end

  def test_projection_errors_do_not_print_configuration_or_credentials
    rewrite("service: leapview", "service: 'private bad service'")
    output, error, status = Open3.capture3(RbConfig.ruby, "-W0", File.join(__dir__, "maintenance_config.rb"))
    refute status.success?
    assert_empty output
    assert_equal "Managed Kamal configuration validation failed\n", error
  end

  def test_mutation_requires_the_inherited_exclusive_controller_lock
    File.open("controller.lock", File::RDWR | File::CREAT, 0o600) do |lock|
      lock.flock(File::LOCK_EX)
      env = {"LEAPVIEW_MANAGED_LOCK_FD" => "3", "LEAPVIEW_MANAGED_LOCK_PATH" => File.join(@directory, "controller.lock")}
      code = "require #{File.join(__dir__, 'maintenance_adapter.rb').inspect}; LeapViewManagedMaintenance.require_controller_lock!; puts 'locked'"
      output, error, status = Open3.capture3(env, RbConfig.ruby, "-W0", "-e", code, 3 => lock)
      assert status.success?, error
      assert_equal "locked\n", output
      # Opening the same path afresh is a different flock ownership identity.
      File.open("controller.lock", File::RDWR) do |other|
        output, _error, status = Open3.capture3(env, RbConfig.ruby, "-W0", "-e", code, 3 => other)
        refute status.success?
        assert_empty output
      end
      env["LEAPVIEW_MANAGED_LOCK_PATH"] = File.join(@directory, "gate.json")
      _output, _error, status = Open3.capture3(env, RbConfig.ruby, "-W0", "-e", code, 3 => lock)
      refute status.success?
    end
  end

  def test_scoped_cli_mutation_uses_verified_flock_instead_of_durable_kamal_lock
    parent = Class.new do
      def modify(lock: false)
        raise "must not acquire Kamal directory lock" if lock
        yield
      end
    end
    subject = Class.new(parent) { prepend LeapViewManagedMaintenance::ControllerLock }.new
    ENV.delete("LEAPVIEW_MANAGED_LOCK_FD")
    assert_raises(KeyError) { subject.send(:modify, lock: true) { flunk "unlocked mutation" } }
    File.open("controller.lock", File::RDWR | File::CREAT, 0o600) do |lock|
      lock.flock(File::LOCK_EX)
      ENV["LEAPVIEW_MANAGED_LOCK_FD"] = lock.fileno.to_s
      ENV["LEAPVIEW_MANAGED_LOCK_PATH"] = File.join(@directory, "controller.lock")
      assert_equal :mutated, subject.send(:modify, lock: true) { :mutated }
    end
  end

  def test_proxy_removal_is_exact_idempotent_and_preserves_docker_failures
    command = Kamal::Commands::Proxy.new(config, host: "127.0.0.1").remove_container.join(" ")
    File.write("docker", <<~SH, perm: 0o700)
      #!/usr/bin/env sh
      if [ "$2" = ls ]; then
        [ "$5" = 'name=^/kamal-proxy$' ] || exit 91
        [ "$FAKE_DOCKER_CASE" = lookup-error ] && exit 42
        [ "$FAKE_DOCKER_CASE" = absent ] || printf 'owned-proxy-id'
      elif [ "$2" = rm ]; then
        [ "$3" = owned-proxy-id ] || exit 92
        [ "$FAKE_DOCKER_CASE" = remove-error ] && exit 43
        printf 'removed'
      else
        exit 93
      fi
    SH
    env = {"PATH" => "#{@directory}:#{ENV.fetch('PATH')}"}
    {"absent" => [0, ""], "present" => [0, "removed"], "lookup-error" => [42, ""], "remove-error" => [43, ""]}.each do |mode, (exit_code, expected)|
      output, error, status = Open3.capture3(env.merge("FAKE_DOCKER_CASE" => mode), "sh", "-c", command)
      assert_equal exit_code, status.exitstatus, error
      assert_equal expected, output
    end
  end
end
