package module

import (
	"context"
	"errors"
	"testing"
)

type subjectResolverStub struct {
	groups []string
	err    error
}

func (s subjectResolverStub) ListGroupIDsForPrincipal(context.Context, string) ([]string, error) {
	return s.groups, s.err
}

func TestAuthorizationSubjectsIncludesPrincipalAndGroups(t *testing.T) {
	subjects, err := authorizationSubjects(t.Context(), "principal-1", subjectResolverStub{groups: []string{"group-a", "group-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 3 || subjects[0].Kind != "principal" || subjects[0].ID != "principal-1" || subjects[1].Kind != "group" || subjects[1].ID != "group-a" || subjects[2].ID != "group-b" {
		t.Fatalf("subjects = %#v", subjects)
	}
}

func TestAuthorizationSubjectsFailsClosedOnLookupAndInvalidGroup(t *testing.T) {
	if _, err := authorizationSubjects(t.Context(), "principal-1", subjectResolverStub{err: errors.New("lookup failed")}); err == nil {
		t.Fatal("lookup failure was accepted")
	}
	if _, err := authorizationSubjects(t.Context(), "principal-1", subjectResolverStub{groups: []string{" bad group "}}); err == nil {
		t.Fatal("invalid group was accepted")
	}
}
