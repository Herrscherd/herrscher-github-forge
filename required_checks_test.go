package github

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	rulesPathPrefix = "/repos/acme/widgets/rules/branches/"
	branchPath      = "/repos/acme/widgets/branches/main"
	protectionPath  = "/repos/acme/widgets/branches/main/protection/required_status_checks"
)

type reply struct {
	status  int
	payload any
}

func notProtectedReply() reply {
	return reply{http.StatusNotFound, map[string]string{"message": "Branch not protected"}}
}

func protectedBranchReply() reply {
	return reply{http.StatusOK, map[string]any{"protected": true}}
}

func unprotectedBranchReply() reply {
	return reply{http.StatusOK, map[string]any{"protected": false}}
}

func serveRequirements(tb testing.TB, rules, protection, branch reply) http.Handler {
	tb.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, rulesPathPrefix):
			writeJSON(tb, w, rules.status, rules.payload)
		case r.URL.Path == protectionPath:
			writeJSON(tb, w, protection.status, protection.payload)
		case r.URL.Path == branchPath:
			writeJSON(tb, w, branch.status, branch.payload)
		default:
			http.NotFound(w, r)
		}
	})
}

func ruleFixture(checks ...map[string]any) map[string]any {
	return map[string]any{
		"type":       "required_status_checks",
		"parameters": map[string]any{"required_status_checks": checks},
	}
}

func ruleCheckFixture(context string, integrationID int64) map[string]any {
	return map[string]any{"context": context, "integration_id": integrationID}
}

func mustBranch(tb testing.TB, text string) contracts.BranchName {
	tb.Helper()
	branch, err := contracts.ParseBranchName(text)
	if err != nil {
		tb.Fatal(err)
	}
	return branch
}

func requiredOf(t *testing.T, handler http.Handler) ([]contracts.RequiredCheck, error) {
	t.Helper()
	f, _ := newTestForge(t, handler)
	return f.RequiredChecks(t.Context(), testRepo(t), mustBranch(t, "main"))
}

func requirementNames(required []contracts.RequiredCheck) []string {
	names := make([]string, 0, len(required))
	for _, requirement := range required {
		names = append(names, requirement.Name())
	}
	return names
}

func TestRequiredChecksReadsTheRulesetOnly(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{ruleFixture(ruleCheckFixture("build", 15368))}},
		notProtectedReply(), protectedBranchReply())
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 || required[0].Name() != "build" || required[0].AppID() != 15368 {
		t.Fatalf("RequiredChecks = %v, want build of app 15368", requirementNames(required))
	}
}

func TestRequiredChecksReadsTheProtectionOnly(t *testing.T) {
	protection := map[string]any{
		"contexts": []string{"lint"},
		"checks":   []map[string]any{{"context": "build", "app_id": 15368}},
	}
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{}},
		reply{http.StatusOK, protection}, protectedBranchReply())
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 2 || required[0].Name() != "lint" || required[0].AppID() != 0 ||
		required[1].Name() != "build" || required[1].AppID() != 15368 {
		t.Fatalf("RequiredChecks = %v, want lint of any app then build of app 15368", requirementNames(required))
	}
}

func TestRequiredChecksDeduplicatesTheTwoSources(t *testing.T) {
	protection := map[string]any{
		"checks": []map[string]any{{"context": "build", "app_id": 15368}, {"context": "lint", "app_id": 0}},
	}
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{ruleFixture(ruleCheckFixture("build", 15368))}},
		reply{http.StatusOK, protection}, protectedBranchReply())
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if got := requirementNames(required); len(got) != 2 || got[0] != "build" || got[1] != "lint" {
		t.Fatalf("RequiredChecks = %v, want build and lint once each", got)
	}
}

func TestRequiredChecksIsEmptyWhenTheBranchIsNotProtected(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{}},
		reply{http.StatusNotFound, map[string]string{"message": "Not Found"}}, unprotectedBranchReply())
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 0 {
		t.Fatalf("RequiredChecks = %v, want none", requirementNames(required))
	}
}

func TestRequiredChecksReadsNoBranchWhenTheProtectionSaysNotProtected(t *testing.T) {
	var branchReads atomic.Int32
	served := serveRequirements(t,
		reply{http.StatusOK, []any{ruleFixture(ruleCheckFixture("test", 0))}},
		notProtectedReply(), protectedBranchReply())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == branchPath {
			branchReads.Add(1)
		}
		served.ServeHTTP(w, r)
	})
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if got := requirementNames(required); len(got) != 1 || got[0] != "test" {
		t.Fatalf("RequiredChecks = %v, want the ruleset requirement only", got)
	}
	if reads := branchReads.Load(); reads != 0 {
		t.Fatalf("branch read %d times, want no read when the protection says not protected", reads)
	}
}

func TestRequiredChecksTakesTheRulesetsWhenTheBranchIsGovernedByThemOnly(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{ruleFixture(ruleCheckFixture("test", 0))}},
		notProtectedReply(), protectedBranchReply())
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if got := requirementNames(required); len(got) != 1 || got[0] != "test" {
		t.Fatalf("RequiredChecks = %v, want the ruleset requirement only", got)
	}
}

func TestRequiredChecksRefusesANotFoundProtectionOnAProtectedBranch(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{}},
		reply{http.StatusNotFound, map[string]string{"message": "Not Found"}}, protectedBranchReply())
	_, err := requiredOf(t, handler)
	if !errors.Is(err, contracts.ErrRequiredChecksUnknown) || !errors.Is(err, contracts.ErrForgeNotFound) || !errors.Is(err, errProtectionUnread) {
		t.Fatalf("RequiredChecks error = %v, want unknown requirements, since the protection cannot be read", err)
	}
}

func TestRequiredChecksRefusesAnUnknownBranch(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{}},
		reply{http.StatusNotFound, map[string]string{"message": "Not Found"}},
		reply{http.StatusNotFound, map[string]string{"message": "Not Found"}})
	_, err := requiredOf(t, handler)
	if !errors.Is(err, contracts.ErrRequiredChecksUnknown) || !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("RequiredChecks error = %v, want unknown requirements caused by a missing branch", err)
	}
}

func TestRequiredChecksWrapsARefusedRulesetRead(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusForbidden, map[string]string{"message": "Forbidden"}},
		reply{http.StatusOK, map[string]any{"checks": []any{}}}, protectedBranchReply())
	_, err := requiredOf(t, handler)
	if !errors.Is(err, contracts.ErrRequiredChecksUnknown) || !errors.Is(err, contracts.ErrForgeForbidden) {
		t.Fatalf("RequiredChecks error = %v, want unknown requirements caused by a refusal", err)
	}
}

func TestRequiredChecksWrapsARefusedProtectionRead(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{}},
		reply{http.StatusForbidden, map[string]string{"message": "Forbidden"}}, protectedBranchReply())
	_, err := requiredOf(t, handler)
	if !errors.Is(err, contracts.ErrRequiredChecksUnknown) || !errors.Is(err, contracts.ErrForgeForbidden) {
		t.Fatalf("RequiredChecks error = %v, want unknown requirements caused by a refusal", err)
	}
}

func TestRequiredChecksRefusesARequirementWithoutAName(t *testing.T) {
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{ruleFixture(ruleCheckFixture("", 0))}},
		notProtectedReply(), protectedBranchReply())
	_, err := requiredOf(t, handler)
	if !errors.Is(err, contracts.ErrEmptyRequiredCheckName) || !errors.Is(err, contracts.ErrRequiredChecksUnknown) {
		t.Fatalf("RequiredChecks error = %v, want an unnamed requirement refused as unknown", err)
	}
}

func TestRequiredChecksReadsAppMinusOneAsAnySource(t *testing.T) {
	protection := map[string]any{
		"checks": []map[string]any{{"context": "lint", "app_id": -1}},
	}
	handler := serveRequirements(t,
		reply{http.StatusOK, []any{ruleFixture(ruleCheckFixture("build", -1))}},
		reply{http.StatusOK, protection}, protectedBranchReply())
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 2 || required[0].AppID() != 0 || required[1].AppID() != 0 {
		t.Fatalf("RequiredChecks = %v, want both requirements satisfiable by any app", requirementNames(required))
	}
}

func TestRequiredChecksReadsEveryPageOfTheRuleset(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == branchPath:
			writeJSON(t, w, http.StatusOK, map[string]any{"protected": true})
		case r.URL.Path == protectionPath:
			writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "Branch not protected"})
		case !strings.HasPrefix(r.URL.Path, rulesPathPrefix):
			http.NotFound(w, r)
		case r.URL.Query().Get("page") == "2":
			writeJSON(t, w, http.StatusOK, []any{ruleFixture(ruleCheckFixture("lint", 0))})
		default:
			w.Header().Set(headerLink, `<?page=2>; rel="next"`)
			writeJSON(t, w, http.StatusOK, []any{ruleFixture(ruleCheckFixture("build", 15368))})
		}
	})
	required, err := requiredOf(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	if got := requirementNames(required); len(got) != 2 || got[0] != "build" || got[1] != "lint" {
		t.Fatalf("RequiredChecks = %v, want build from page one and lint from page two", got)
	}
}
