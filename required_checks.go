package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const requiredStatusChecksRule = "required_status_checks"

type ruleWire struct {
	Type       string         `json:"type"`
	Parameters ruleParamsWire `json:"parameters"`
}

type ruleParamsWire struct {
	RequiredStatusChecks []ruleCheckWire `json:"required_status_checks"`
}

type ruleCheckWire struct {
	Context       string `json:"context"`
	IntegrationID int64  `json:"integration_id"`
}

type protectionWire struct {
	Contexts []string              `json:"contexts"`
	Checks   []protectionCheckWire `json:"checks"`
}

type protectionCheckWire struct {
	Context string `json:"context"`
	AppID   int64  `json:"app_id"`
}

type requirementKey struct {
	name  string
	appID int64
}

func (f *forge) RequiredChecks(ctx context.Context, repo contracts.ForgeRepoRef, branch contracts.BranchName) ([]contracts.RequiredCheck, error) {
	if err := f.checkRepo(repo); err != nil {
		return nil, err
	}
	if branch.IsZero() {
		return nil, errMissingBranch
	}
	fromRules, _, err := readListing(ctx, f, f.rulesListing(repo, branch), ruleRequirements)
	if err != nil {
		return nil, unknownRequirements(branch, err)
	}
	fromProtection, err := f.protectionRequirements(ctx, repo, branch)
	if err != nil {
		return nil, unknownRequirements(branch, err)
	}
	return mergeRequirements(fromRules, fromProtection), nil
}

func (f *forge) rulesListing(repo contracts.ForgeRepoRef, branch contracts.BranchName) func(int) *url.URL {
	return func(number int) *url.URL {
		return f.pagedRepoURL(repo, url.Values{}, number, branchElements(branch, "rules", "branches")...)
	}
}

func (f *forge) protectionRequirements(ctx context.Context, repo contracts.ForgeRepoRef, branch contracts.BranchName) ([]contracts.RequiredCheck, error) {
	elements := protectionElements(branch)
	var wire protectionWire
	err := f.getJSON(ctx, f.repoURL(repo, nil, elements...), &wire)
	if errors.Is(err, contracts.ErrForgeNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return requirementsOfProtection(wire)
}

func branchElements(branch contracts.BranchName, prefix ...string) []string {
	return append(prefix, strings.Split(branch.String(), "/")...)
}

func protectionElements(branch contracts.BranchName) []string {
	elements := append([]string{"branches"}, strings.Split(branch.String(), "/")...)
	return append(elements, "protection", "required_status_checks")
}

func ruleRequirements(body io.Reader) ([]contracts.RequiredCheck, error) {
	var rules []ruleWire
	if err := decodeJSON(body, &rules); err != nil {
		return nil, err
	}
	var required []contracts.RequiredCheck
	for _, rule := range rules {
		if rule.Type != requiredStatusChecksRule {
			continue
		}
		for _, check := range rule.Parameters.RequiredStatusChecks {
			requirement, err := contracts.NewRequiredCheck(check.Context, check.IntegrationID)
			if err != nil {
				return nil, err
			}
			required = append(required, requirement)
		}
	}
	return required, nil
}

func requirementsOfProtection(wire protectionWire) ([]contracts.RequiredCheck, error) {
	entries := make([]protectionCheckWire, 0, len(wire.Contexts)+len(wire.Checks))
	for _, name := range wire.Contexts {
		entries = append(entries, protectionCheckWire{Context: name})
	}
	entries = append(entries, wire.Checks...)
	required := make([]contracts.RequiredCheck, 0, len(entries))
	for _, entry := range entries {
		requirement, err := contracts.NewRequiredCheck(entry.Context, entry.AppID)
		if err != nil {
			return nil, err
		}
		required = append(required, requirement)
	}
	return required, nil
}

func mergeRequirements(groups ...[]contracts.RequiredCheck) []contracts.RequiredCheck {
	seen := make(map[requirementKey]bool)
	var merged []contracts.RequiredCheck
	for _, group := range groups {
		for _, requirement := range group {
			key := requirementKey{name: requirement.Name(), appID: requirement.AppID()}
			if !seen[key] {
				seen[key] = true
				merged = append(merged, requirement)
			}
		}
	}
	return merged
}

func unknownRequirements(branch contracts.BranchName, cause error) error {
	return fmt.Errorf("required checks of branch %s: %w: %w", branch, contracts.ErrRequiredChecksUnknown, cause)
}
