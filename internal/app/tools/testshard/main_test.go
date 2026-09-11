package main

import (
	"reflect"
	"testing"
)

func TestListTestArgsWithoutTagsPreservesOrdinaryShardBehavior(t *testing.T) {
	got := listTestArgs("./internal/app", "")
	want := []string{"test", "-list", "^Test", "./internal/app"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listTestArgs() = %#v, want %#v", got, want)
	}
}

func TestListTestArgsForwardsBuildTags(t *testing.T) {
	got := listTestArgs("./internal/app", "integration duckdb_arrow")
	want := []string{"test", "-tags", "integration duckdb_arrow", "-list", "^Test", "./internal/app"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listTestArgs() = %#v, want %#v", got, want)
	}
}

func TestListTestArgsOmitsWhitespaceOnlyTags(t *testing.T) {
	got := listTestArgs("./internal/app", " \t")
	want := []string{"test", "-list", "^Test", "./internal/app"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listTestArgs() = %#v, want %#v", got, want)
	}
}
