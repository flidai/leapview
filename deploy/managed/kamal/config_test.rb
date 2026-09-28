# Native Kamal parsing and command construction; never contacts a host/registry.
require "bundler/setup"
require_relative "probe_host"
require "minitest/autorun"
require "pathname"

class ManagedKamalConfigurationTest < Minitest::Test
  def setup
    ENV["KAMAL_APP_HOST"] = "example-app.invalid"
    ENV["KAMAL_APP_HOSTNAME"] = "dash.example.com"
    ENV["KAMAL_REGISTRY_USERNAME"] = "example"
    ENV["LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID"] = "fixture-pool"
    ENV["LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST"] = "fixture-digest"
    @config = Kamal::Configuration.create_from(
      config_file: Pathname.new(__dir__).join("deploy.yml.example"),
      version: "0123456789abcdef"
    )
  end

  def test_streaming_and_readiness
    assert_equal "/readyz", @config.raw_config.proxy["healthcheck"]["path"]
    assert_equal false, @config.raw_config.proxy["buffering"]["responses"]
    assert_equal false, @config.raw_config.proxy["forward_headers"]
    assert_equal 8080, @config.raw_config.proxy["app_port"]
    assert_equal "v0.9.2", @config.raw_config.proxy["run"]["version"]
    assert_equal 120, @config.stop_timeout
  end

  def test_persistent_storage_and_runtime_privileges
    assert_includes @config.volume_args, "/var/lib/leapview:/var/lib/leapview"
    secrets = @config.raw_config.env["secret"]
    assert_includes secrets, "LEAPVIEW_POSTGRES_CONTROL_URL"
    assert_includes secrets, "LEAPVIEW_AGENT_CREDENTIAL_KEY"
    assert_includes secrets, "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL"
    assert_includes secrets, "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL"
    refute secrets.any? { |key| key.match?(/MIGRATOR|UPGRADE_COORDINATOR|ADMIN|HCLOUD|BACKUP/) }
    assert_empty @config.accessories
    assert_equal "ghcr.io/flidai/leapview:0123456789abcdef", @config.absolute_image
  end

  def test_probe_uses_the_public_host_without_relaxing_application_host_checks
    proxy = Kamal::Configuration::Proxy.new(config: @config, proxy_config: @config.raw_config.proxy, secrets: {})
    assert_includes proxy.deploy_command_args(target: "container-id"), '--health-check-host="dash.example.com"'
  end

  def test_missing_inventory_fails_closed
    ENV.delete("KAMAL_APP_HOST")
    assert_raises(KeyError) do
      Kamal::Configuration.create_from(config_file: Pathname.new(__dir__).join("deploy.yml.example"))
    end
  end
end
