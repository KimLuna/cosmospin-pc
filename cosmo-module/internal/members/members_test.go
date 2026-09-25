package members

import "testing"

func TestNormalizeGroup(t *testing.T) {
	if g, ok := NormalizeGroup("TRIPLES"); !ok || g != "tripleS" {
		t.Fatalf("got %q %v", g, ok)
	}
	if _, ok := NormalizeGroup("bts"); ok {
		t.Fatalf("unknown group should not resolve")
	}
}

func TestResolveMember(t *testing.T) {
	// by name (case-insensitive) -> correct api id (SeoAh is #23, apiID 28)
	if m, ok := ResolveMember("tripleS", "seoah"); !ok || m.APIID != 28 || m.Number != 23 {
		t.Fatalf("got %+v %v", m, ok)
	}
	// by official number
	if m, ok := ResolveMember("tripleS", "3"); !ok || m.Name != "JiWoo" || m.APIID != 3 {
		t.Fatalf("got %+v %v", m, ok)
	}
	if _, ok := ResolveMember("tripleS", "nobody"); ok {
		t.Fatalf("unknown member should not resolve")
	}
}

func TestAllMembersOrdered(t *testing.T) {
	all := AllMembers("artms")
	if len(all) != 5 || all[0].Name != "HeeJin" || all[4].Name != "Choerry" {
		t.Fatalf("unexpected artms order: %+v", all)
	}
}
