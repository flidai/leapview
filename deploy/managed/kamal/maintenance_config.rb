# This private projection contains resolved credentials. The controller captures
# stdout in memory; never run it with output logging or publish its output.
require_relative "maintenance_adapter"
require "pathname"

begin
  config = Kamal::Configuration.create_from(
    config_file: Pathname.new(ENV.fetch("LEAPVIEW_MANAGED_CONFIG", "deploy.yml")),
    version: ENV.fetch("LEAPVIEW_MANAGED_REVISION")
  )
  role = config.role("web")
  puts JSON.generate(
    service: config.service,
    hosts: role.hosts,
    roles: config.roles.map(&:name),
    hostname: config.proxy.hosts.first,
    volumes: config.raw_config.volumes,
    environment: role.env("127.0.0.1").to_h.transform_values(&:to_s),
    image: config.absolute_image,
    proxyImage: config.proxy.run.image,
    healthcheckPath: "/maintenance/readyz"
  )
rescue StandardError
  # Parser errors may include source configuration or resolved secret values.
  $stderr.puts "Managed Kamal configuration validation failed"
  exit 1
end
