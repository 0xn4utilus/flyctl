package presenters

import (
	"github.com/superfly/flyctl/api"
)

type Secrets struct {
	Secrets []api.Secret
}

func (p *Secrets) FieldNames() []string {
	return []string{"Name", "Digest", "Date"}
}

func (p *Secrets) FieldMap() map[string]string {
	return map[string]string{
		"Name":   "Name",
		"Digest": "Digest",
		"Date":   "Date",
	}
}

func (p *Secrets) Records() []map[string]string {
	out := []map[string]string{}

	for _, secret := range p.Secrets {
		out = append(out, map[string]string{
			"Name":   secret.Name,
			"Digest": secret.Digest,
			"Date":   formatRelativeTime(secret.CreatedAt),
		})
	}

	return out
}
