package hostinstall

import "testing"

func TestMaintenanceProfileRejectsAmbiguousAndPublicBindings(t *testing.T) {
	for name, change := range map[string]func(*MaintenanceProfile){
		"named-port":       func(p *MaintenanceProfile) { p.HTTPSBinding = "127.0.0.1:https" },
		"wrong-engine":     func(p *MaintenanceProfile) { p.PostgresImage = "postgres:180@sha256:" + hex64('a') },
		"public":           func(p *MaintenanceProfile) { p.HTTPSBinding = "0.0.0.0:8443" },
		"collision":        func(p *MaintenanceProfile) { p.RehearsalBinding = p.HTTPSBinding },
		"ambiguous-volume": func(p *MaintenanceProfile) { p.Volumes["home"] = p.Volumes["postgres"] },
		"root":             func(p *MaintenanceProfile) { p.StateRoot = p.Root },
		"service":          func(p *MaintenanceProfile) { p.AppService = p.ProxyService },
		"mutable-engine":   func(p *MaintenanceProfile) { p.PostgresImage = "postgres:18" },
	} {
		t.Run(name, func(t *testing.T) {
			p := profileFixture()
			change(&p)
			if p.Validate() == nil {
				t.Fatal("unsafe profile accepted")
			}
		})
	}
}

func TestSourceCompatibilityUsesHistoryAndEngineIdentities(t *testing.T) {
	r := nativeRequestFixture(t)
	before, after := r.Plan.SourceBefore, r.Plan.SourceAfter
	mode, pending, err := classifySources(before, after)
	if err != nil || mode != "database-upgrade-required" || len(pending) != 2 {
		t.Fatalf("%s %v %v", mode, pending, err)
	}
	before = after
	mode, _, err = classifySources(before, after)
	if err != nil || mode != "image-only" {
		t.Fatalf("identical compatibility: %s %v", mode, err)
	}
	after.RolePolicy = hex64('e')
	mode, _, err = classifySources(before, after)
	if err != nil || mode != "database-upgrade-required" {
		t.Fatalf("policy-only: %s %v", mode, err)
	}
	after.Engines = map[string]string{"river": "changed"}
	mode, _, err = classifySources(before, after)
	if err != nil || mode != "review-required" {
		t.Fatalf("engine: %s %v", mode, err)
	}
}
