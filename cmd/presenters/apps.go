package presenters

import "github.com/superfly/flyctl/api"

type Apps struct {
	App  *api.App
	Apps []api.App
}

func (p *Apps) FieldNames() []string {
	return []string{"Name", "Owner"}
}

func (p *Apps) FieldMap() map[string]string {
	return map[string]string{
		"Name":  "Name",
		"Owner": "Owner",
	}
}

func (p *Apps) Records() []map[string]string {
	out := []map[string]string{}

	if p.App != nil {
		p.Apps = append(p.Apps, *p.App)
	}

	for _, app := range p.Apps {
		out = append(out, map[string]string{
			"Name":  app.Name,
			"Owner": app.Organization.Slug,
		})
	}

	return out
}
