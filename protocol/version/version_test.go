package version

import (
	"errors"
	"testing"
)

func TestForeverIdentity(t *testing.T) {
	v := Forever()
	if got, want := v.VersionString(), "1.60.1"; got != want {
		t.Fatalf("VersionString() = %q, want %q", got, want)
	}
	if got, want := v.String(), "wowf 1.60.1.70755"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestKnownBuilds(t *testing.T) {
	for _, b := range []int{70707, 70755} {
		v, ok := ByBuild(b)
		if !ok || v.Build != b || v.Product != "wowf" {
			t.Fatalf("ByBuild(%d) = %+v, %v", b, v, ok)
		}
		if p, ok := Parse(v.String()); !ok || p != v {
			t.Fatalf("Parse(%q) = %+v, %v", v.String(), p, ok)
		}
		if err := v.Check(v); err != nil {
			t.Fatalf("%s self check: %v", v, err)
		}
	}
	if Build70707.String() != "wowf 1.60.1.70707" || Build70707.BuildKey != "7feb61f3c994bcb168526b8db521514a" {
		t.Fatalf("70707 identity changed: %+v", Build70707)
	}
	if _, ok := ByBuild(70658); ok {
		t.Fatal("70658 is not a known build")
	}
}

func TestCheckRequiresExactBuild(t *testing.T) {
	v := Forever()
	if err := v.Check(Forever()); err != nil {
		t.Fatalf("self check failed: %v", err)
	}
	if err := v.Check(Build70707); !errors.Is(err, ErrBuildMismatch) {
		t.Fatalf("70755 vs 70707: expected ErrBuildMismatch, got %v", err)
	}
	other := Forever()
	other.Build++
	if err := v.Check(other); !errors.Is(err, ErrBuildMismatch) {
		t.Fatalf("expected ErrBuildMismatch, got %v", err)
	}
	if err := other.Check(other); !errors.Is(err, ErrUnknownBuild) {
		t.Fatalf("unknown build self check: expected ErrUnknownBuild, got %v", err)
	}
	if v.Compatible(other) {
		t.Fatal("different builds must not be compatible")
	}
}

func TestRegistryLookup(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() is empty")
	}
	if got := Latest(); got.Build != all[len(all)-1].Build {
		t.Fatalf("Latest() = %s, want newest of %v", got, all)
	}
	ref := Forever()
	got, ok := Lookup(ref.Build)
	if !ok || got != ref {
		t.Fatalf("Lookup(%d) = %v, %v; want %v", ref.Build, got, ok, ref)
	}
	if _, ok := Lookup(-1); ok {
		t.Fatal("Lookup(-1) should fail")
	}
}
