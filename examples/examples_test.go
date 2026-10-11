package examples

import (
	"testing"
)

func TestBrandPacksParse(t *testing.T) {
	names := BrandPacks()
	if len(names) == 0 {
		t.Fatal("no example brand packs embedded")
	}
	for _, n := range names {
		p, err := BrandPack(n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if p.Name == "" || len(p.Profile.Services) == 0 || len(p.Keywords.Track) == 0 || len(p.ICPs) == 0 {
			t.Errorf("%s: example pack is incomplete: %+v", n, p)
		}
	}
	if _, err := BrandPack("nope"); err == nil {
		t.Error("unknown pack must be an error")
	}
}
