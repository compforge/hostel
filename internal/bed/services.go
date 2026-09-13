package bed

import "maps"

// ServiceSpec is non-secret desired state. TemplateDigest pins the deployment
// definition across local recovery; runtime addresses never enter snapshots.
type ServiceSpec struct {
	Name           string            `json:"name"`
	Template       string            `json:"template"`
	Env            map[string]string `json:"env,omitempty"`
	TemplateDigest string            `json:"template_digest,omitempty"`
}

func CloneServices(in []ServiceSpec) []ServiceSpec {
	out := make([]ServiceSpec, len(in))
	for i, s := range in {
		s.Env = maps.Clone(s.Env)
		out[i] = s
	}
	return out
}
