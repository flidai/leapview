output "hosts" {
  description = "Copy these non-secret addresses into private NixOS/Kamal inventory."
  value = { for role, host in hcloud_server.host : role => {
    name       = host.name
    public_ip  = host.ipv4_address
    private_ip = hcloud_server_network.host[role].ip
  } }
}
