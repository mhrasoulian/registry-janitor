package prune

import "github.com/mhrasoulian/registry-janitor/internal/policy"

// Plan mirrors the policy evaluation and is intentionally small at this milestone.
type Plan struct {
	Repo       string
	Tags       []policy.TagDecision
	DeleteList []string
}

func NewPlan(plan policy.RepositoryPlan) Plan {
	return Plan{
		Repo:       plan.Repo,
		Tags:       plan.Tags,
		DeleteList: plan.DigestsToDelete,
	}
}
