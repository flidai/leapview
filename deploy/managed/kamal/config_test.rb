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
    ENV["LEAPVIEW_OIDC_ISSUER_URL"] = "https://login.microsoftonline.com/fixture-tenant/v2.0"
    ENV["LEAPVIEW_OIDC_CLIENT_ID"] = "fixture-client"
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

  def test_managed_disclosure_and_sign_in_baseline
    clear = @config.raw_config.env["clear"]
    assert_equal "0", clear["LEAPVIEW_LOCAL_AUTH"]
    assert_equal "false", clear["LEAPVIEW_MCP_ENABLED"]
    assert_equal ":9090", clear["LEAPVIEW_METRICS_ADDR"]
    assert_equal "https://dash.example.com/auth/oidc/callback", clear["LEAPVIEW_OIDC_CALLBACK_URL"]
    assert_includes @config.raw_config.env["secret"], "LEAPVIEW_OIDC_CLIENT_SECRET"
    refute @config.raw_config.servers["web"]["options"].key?("publish")
  end

  def test_missing_inventory_fails_closed
    ENV.delete("KAMAL_APP_HOST")
    assert_raises(KeyError) do
      Kamal::Configuration.create_from(config_file: Pathname.new(__dir__).join("deploy.yml.example"))
    end
  end

  def test_missing_customer_identity_fails_closed
    %w[LEAPVIEW_OIDC_ISSUER_URL LEAPVIEW_OIDC_CLIENT_ID].each do |key|
      previous = ENV.delete(key)
      begin
        assert_raises(KeyError) do
          Kamal::Configuration.create_from(config_file: Pathname.new(__dir__).join("deploy.yml.example"))
        end
      ensure
        ENV[key] = previous
      end
    end
  end
end
