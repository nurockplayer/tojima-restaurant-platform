package testguest

import "testing"

func TestGuestDisplayNameFixtureOptionPreservesDemoNull(t *testing.T) {
	name := "公開店舗"
	if fixtureGuestDisplayName(Options{}) != nil {
		t.Fatal("existing fixtures must stay NULL")
	}
	if got := fixtureGuestDisplayName(Options{GuestDisplayName: &name}); got == nil || *got != name {
		t.Fatal("non-demo name missing")
	}
	if fixtureGuestDisplayName(Options{DemoShironagasukujira: true, GuestDisplayName: &name}) != nil {
		t.Fatal("demo must stay NULL")
	}
}
