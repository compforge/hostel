package bed

import "testing"

func TestPortMappingDeclarations(t *testing.T) {
	for _, input := range [][]PortMappingSpec{
		{{Name: "bad name"}}, {{Name: "p", Protocol: "udp"}}, {{Name: "p", BedPort: -1}}, {{Name: "p", RequireBedPort: true}},
		{{Name: "p"}, {Name: "p"}}, {{Name: "a", BedPort: 8080, RequireBedPort: true}, {Name: "b", BedPort: 8080, RequireBedPort: true}},
	} {
		if _, err := NormalizePortMappings(input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	input := []PortMappingSpec{{Name: "b", BedPort: 8080}, {Name: "a", BedPort: 8080}}
	normalized, err := NormalizePortMappings(input)
	if err != nil {
		t.Fatal(err)
	}
	if normalized[0].Name != "a" || normalized[0].Protocol != "tcp" || input[0].Protocol != "" {
		t.Fatal("normalization changed input")
	}
	b := New("ports", "", Spec{PortMappings: normalized})
	normalized[0].BedPort = 99
	copy := b.Spec()
	copy.PortMappings[0].BedPort = 88
	if b.Spec().PortMappings[0].BedPort != 8080 {
		t.Fatal("Bed declaration aliases caller storage")
	}
}
