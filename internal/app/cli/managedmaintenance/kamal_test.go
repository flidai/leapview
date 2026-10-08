package managedmaintenance

import (
	"strings"
	"testing"
)

func TestManagedInventoryRejectsAlternateIngressAndOwners(t *testing.T) {
	p := HostProfile{Service: "leapview", Home: "/var/lib/leapview/home", Socket: "/var/lib/leapview/home/maintenance.sock", ProxyImage: "basecamp/kamal-proxy@sha256:" + strings.Repeat("c", 64)}
	r := Request{Predecessor: Release{Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64), Revision: strings.Repeat("a", 40)}, Candidate: Release{Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64), Revision: strings.Repeat("b", 40)}}
	app := containerInfo{Name: "/leapview-web-" + r.Predecessor.Revision}
	app.Config.Image = r.Predecessor.Image
	app.Config.Labels = map[string]string{"service": "leapview", "role": "web"}
	app.Config.Env = []string{"LEAPVIEW_HOME=" + p.Home, "LEAPVIEW_MAINTENANCE_SOCKET=" + p.Socket}
	app.HostConfig.NetworkMode = "kamal"
	app.Mounts = []mountInfo{{Type: "bind", Source: "/var/lib/leapview", Destination: "/var/lib/leapview", RW: true}}
	app.State.Running = true
	proxy := containerInfo{Name: "/kamal-proxy"}
	proxy.Config.Image = p.ProxyImage
	proxy.HostConfig.NetworkMode = "kamal"
	proxy.State.Running = true
	if err := validateInventory(p, r, []containerInfo{app, proxy}, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"direct-port", "host-network", "extra-owner", "foreign-proxy", "wrong-home"} {
		t.Run(name, func(t *testing.T) {
			a, b := app, proxy
			items := []containerInfo{}
			switch name {
			case "direct-port":
				a.HostConfig.PortBindings = map[string][]portBinding{"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}}}
			case "host-network":
				a.HostConfig.NetworkMode = "host"
			case "extra-owner":
				items = append(items, app)
			case "foreign-proxy":
				b.Config.Image = "caddy:latest"
			case "wrong-home":
				a.Mounts = []mountInfo{{Type: "bind", Source: "/other", Destination: "/var/lib/leapview", RW: true}}
			}
			items = append(items, a, b)
			if validateInventory(p, r, items, false) == nil {
				t.Fatal("accepted unfenced topology")
			}
		})
	}
}

func TestRuntimeEnvironmentIdentityIgnoresOnlyKamalVersionMetadata(t *testing.T) {
	a := []string{"LEAPVIEW_HOME=/state", "LEAPVIEW_AGENT_CREDENTIAL_KEY=private", "KAMAL_VERSION=old"}
	b := []string{"KAMAL_VERSION=new", "LEAPVIEW_AGENT_CREDENTIAL_KEY=private", "LEAPVIEW_HOME=/state"}
	if environmentIdentity(a) != environmentIdentity(b) {
		t.Fatal("Kamal version changed runtime configuration identity")
	}
	b[1] = "LEAPVIEW_AGENT_CREDENTIAL_KEY=changed"
	if environmentIdentity(a) == environmentIdentity(b) {
		t.Fatal("credential drift accepted")
	}
}
