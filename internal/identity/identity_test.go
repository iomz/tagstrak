package identity_test

import (
	"errors"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
)

func sgtin(t *testing.T, gcp, itemref, serial string) identity.Value {
	t.Helper()
	v, err := identity.New(identity.AuthorityGS1, identity.KeyTypeSGTIN, []identity.Component{
		{Name: identity.GS1CompanyPrefix, Value: gcp},
		{Name: identity.ItemReference, Value: itemref},
		{Name: identity.Serial, Value: serial},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestCanonicalOrderIsEnforced checks that nothing is reordered or defaulted.
func TestCanonicalOrderIsEnforced(t *testing.T) {
	_, err := identity.New(identity.AuthorityGS1, identity.KeyTypeSGTIN, []identity.Component{
		{Name: identity.Serial, Value: "1"},
		{Name: identity.GS1CompanyPrefix, Value: "0614141"},
		{Name: identity.ItemReference, Value: "812345"},
	})
	if !errors.Is(err, identity.ErrComponentLayout) {
		t.Errorf("out of order: error = %v", err)
	}
	_, err = identity.New(identity.AuthorityGS1, identity.KeyTypeSGTIN, []identity.Component{
		{Name: identity.GS1CompanyPrefix, Value: "0614141"},
	})
	if !errors.Is(err, identity.ErrComponentLayout) {
		t.Errorf("missing components: error = %v", err)
	}
	if _, err := identity.New(identity.AuthorityGS1, identity.KeyTypeUnknown, nil); !errors.Is(err, identity.ErrUnknownKeyType) {
		t.Errorf("unknown key type: error = %v", err)
	}
}

// TestEqualityAndKeyAgree checks the structural equality contract, including
// that Key never conflates distinct component splits.
func TestEqualityAndKeyAgree(t *testing.T) {
	a := sgtin(t, "0614141", "812345", "6789")
	b := sgtin(t, "0614141", "812345", "6789")
	c := sgtin(t, "061414", "1812345", "6789") // the same digits, split differently
	d := sgtin(t, "0614141", "812345", "6790")

	if !a.Equal(b) || a.Key() != b.Key() {
		t.Error("equal identities are not equal")
	}
	if a.Equal(c) || a.Key() == c.Key() {
		t.Error("a different company prefix split compares equal")
	}
	if a.Equal(d) || a.Key() == d.Key() {
		t.Error("different serials compare equal")
	}

	// The key must be usable directly as a map key.
	seen := map[identity.Key]int{a.Key(): 1}
	seen[b.Key()]++
	seen[c.Key()]++
	if seen[a.Key()] != 2 || len(seen) != 2 {
		t.Errorf("key map behaviour = %v", seen)
	}
}

// TestValueIsImmutable checks that no accessor hands out a mutable reference.
func TestValueIsImmutable(t *testing.T) {
	comps := []identity.Component{
		{Name: identity.GS1CompanyPrefix, Value: "0614141"},
		{Name: identity.ItemReference, Value: "812345"},
		{Name: identity.Serial, Value: "6789"},
	}
	v, err := identity.New(identity.AuthorityGS1, identity.KeyTypeSGTIN, comps)
	if err != nil {
		t.Fatal(err)
	}
	comps[0].Value = "tampered"
	if got := v.At(0).Value; got != "0614141" {
		t.Errorf("value changed with the caller's slice: %s", got)
	}
	count := 0
	for range v.Components() {
		count++
	}
	if count != 3 {
		t.Errorf("iterated %d components", count)
	}
}

// TestPatternStarSuffixRule pins the rule that makes prefix matching well
// defined.
func TestPatternStarSuffixRule(t *testing.T) {
	fields := []identity.FieldPattern{
		{Name: identity.GS1CompanyPrefix, Kind: identity.FieldAny},
		{Name: identity.ItemReference, Kind: identity.FieldExact, Text: "812345"},
		{Name: identity.Serial, Kind: identity.FieldAny},
	}
	_, err := identity.NewPattern(identity.LevelIdentity, identity.AuthorityGS1, identity.KeyTypeSGTIN, "", fields, nil, "")
	if !errors.Is(err, identity.ErrPatternStar) {
		t.Errorf("error = %v, want ErrPatternStar", err)
	}
}

// TestIdentityPatternsRejectEncodingParts checks the level split is enforced
// by construction, not by convention.
func TestIdentityPatternsRejectEncodingParts(t *testing.T) {
	fields := []identity.FieldPattern{
		{Name: identity.GS1CompanyPrefix, Kind: identity.FieldAny},
		{Name: identity.ItemReference, Kind: identity.FieldAny},
		{Name: identity.Serial, Kind: identity.FieldAny},
	}
	if _, err := identity.NewPattern(identity.LevelIdentity, identity.AuthorityGS1, identity.KeyTypeSGTIN, "sgtin-96", fields, nil, ""); !errors.Is(err, identity.ErrPatternLevel) {
		t.Errorf("identity pattern with a scheme: error = %v", err)
	}
	bits, err := identity.NewBitPattern([]byte{0x30}, []byte{0xFF}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.NewPattern(identity.LevelIdentity, identity.AuthorityGS1, identity.KeyTypeSGTIN, "", fields, bits, ""); !errors.Is(err, identity.ErrPatternLevel) {
		t.Errorf("identity pattern with a bit test: error = %v", err)
	}
	if _, err := identity.NewPattern(identity.LevelEncoding, identity.AuthorityGS1, identity.KeyTypeSGTIN, "", fields, nil, ""); !errors.Is(err, identity.ErrPatternLevel) {
		t.Errorf("encoding pattern without a scheme: error = %v", err)
	}
}

// TestProgrammaticISOPatterns checks the path ISO has to patterns: built from
// typed parts, since no ISO pattern grammar exists to parse.
func TestProgrammaticISOPatterns(t *testing.T) {
	fields := []identity.FieldPattern{
		{Name: identity.OwnerCode, Kind: identity.FieldExact, Text: "CSQ"},
		{Name: identity.EquipmentCategory, Kind: identity.FieldAny},
		{Name: identity.ContainerSerial, Kind: identity.FieldAny},
		{Name: identity.CheckDigit, Kind: identity.FieldAny},
	}
	p, err := identity.NewPattern(identity.LevelIdentity, identity.AuthorityISO6346,
		identity.KeyTypeFreightContainer, "", fields, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	match, err := identity.New(identity.AuthorityISO6346, identity.KeyTypeFreightContainer, []identity.Component{
		{Name: identity.OwnerCode, Value: "CSQ"},
		{Name: identity.EquipmentCategory, Value: "U"},
		{Name: identity.ContainerSerial, Value: "305438"},
		{Name: identity.CheckDigit, Value: "3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.MatchValue(match) {
		t.Error("programmatic ISO pattern did not match")
	}
	if p.MatchValue(sgtin(t, "0614141", "812345", "6789")) {
		t.Error("ISO pattern matched a GS1 identity")
	}
}

// TestRangeMatching pins range semantics.
func TestRangeMatching(t *testing.T) {
	fields := []identity.FieldPattern{
		{Name: identity.GS1CompanyPrefix, Kind: identity.FieldExact, Text: "0614141"},
		{Name: identity.ItemReference, Kind: identity.FieldRange, Lo: 10, Hi: 20},
		{Name: identity.Serial, Kind: identity.FieldAny},
	}
	p, err := identity.NewPattern(identity.LevelIdentity, identity.AuthorityGS1, identity.KeyTypeSGTIN, "", fields, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		itemref string
		want    bool
	}{{"000010", true}, {"000015", true}, {"000020", true}, {"000009", false}, {"000021", false}} {
		if got := p.MatchValue(sgtin(t, "0614141", c.itemref, "1")); got != c.want {
			t.Errorf("itemref %s: match = %v, want %v", c.itemref, got, c.want)
		}
	}
}

// TestNoUniversalStringForm documents the decision that identities have no
// textual serialization: Describe is diagnostic and must not look canonical.
func TestNoUniversalStringForm(t *testing.T) {
	var v any = sgtin(t, "0614141", "812345", "6789")
	if _, ok := v.(interface{ String() string }); ok {
		t.Fatal("identity.Value must not implement fmt.Stringer")
	}
}
