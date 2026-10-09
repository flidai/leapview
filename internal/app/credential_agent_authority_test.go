package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/jackc/pgx/v5"
)

type forbiddenAgentCredentialQuery struct {
	pgx.Tx
	t *testing.T
}

func (tx forbiddenAgentCredentialQuery) QueryRow(context.Context, string, ...any) pgx.Row {
	tx.t.Fatal("invalid boundary reached durable authority")
	return nil
}

func TestAgentCredentialAuthorityPinsExactInstanceActionAndAuthenticatedActor(t *testing.T) {
	instance := "instance_0123456789abcdef0123456789abcdef"
	actor := "00000000-0000-7000-8000-000000000011"
	pair, err := access.NewInstancePermissionPair(access.ActionPlatformSettingsUpdate, instance)
	if err != nil {
		t.Fatal(err)
	}
	check := newAgentCredentialAuthority(instance)
	ctx := accessmodule.WithAPICredential(context.Background(), access.APICredential{Principal: access.Principal{ID: actor}, Token: access.APIToken{ID: "00000000-0000-7000-8000-000000000012", PrincipalID: actor, TokenFingerprint: strings.Repeat("a", 64)}})
	wrongTarget := pair
	wrongTarget.Target.InstanceID = "instance_ffffffffffffffffffffffffffffffff"
	wrongAction := pair
	wrongAction.Action = access.ActionPlatformSettingsRead
	for _, test := range []struct {
		ctx   context.Context
		actor string
		pair  access.PermissionPair
	}{
		{ctx, actor, wrongTarget}, {ctx, actor, wrongAction}, {ctx, "00000000-0000-7000-8000-000000000099", pair}, {context.Background(), actor, pair},
	} {
		if err = check(test.ctx, forbiddenAgentCredentialQuery{t: t}, test.actor, test.pair); !errors.Is(err, access.ErrForbidden) {
			t.Fatalf("invalid authority accepted: %v", err)
		}
	}
	if err = check(ctx, nil, actor, pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("missing transaction accepted")
	}
	if err = newAgentCredentialAuthority("")(ctx, forbiddenAgentCredentialQuery{t: t}, actor, pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("missing installation identity accepted")
	}
}
