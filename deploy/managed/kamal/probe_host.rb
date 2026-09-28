# Kamal 2.12 does not expose kamal-proxy's native --health-check-host option.
# Load with: bundle exec ruby -r ./probe_host.rb -S kamal ...
# Preserve LeapView's allowed-host checks by probing with the configured host.
# Remove this adapter when the pinned Kamal exposes this option natively.
require "bundler/setup"
require "kamal"

module LeapViewProbeHost
  def deploy_options
    options = super
    hosts.empty? ? options : options.merge("health-check-host": hosts.first)
  end
end

Kamal::Configuration::Proxy.prepend(LeapViewProbeHost)
