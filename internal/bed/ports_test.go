package bed

import "testing"

func TestPillowPortDeclarationIsCloned(t *testing.T) {
	b := New("ports", "", Spec{Pillows: []PillowSpec{{Name: "main", Ports: []PillowPort{{Name: "http", ContainerPort: 8080}}}}})
	copy := b.Spec()
	copy.Pillows[0].Ports[0].ContainerPort = 88
	if b.Spec().NetworkPorts()[0].ContainerPort != 8080 {
		t.Fatal("Bed declaration aliases caller storage")
	}
}
