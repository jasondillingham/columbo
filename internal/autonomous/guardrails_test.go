package autonomous

import (
	"strings"
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
)

func TestCheckBlocksFailedLane(t *testing.T) {
	reports := []findings.LaneReport{
		{Lane: "L2 caps", Reverified: []string{"ok"}},
		{Lane: "L6 protocol", Failed: []string{"L6 setup: build failed"}},
	}
	v := Check(reports)
	if v.Proceed() {
		t.Error("a failed lane must block the round")
	}
	if len(v.Blockers) != 1 || !strings.Contains(v.Blockers[0], "L6 protocol") {
		t.Errorf("blocker should name the failed lane: %v", v.Blockers)
	}
}

func TestCheckBlocksEmptyRound(t *testing.T) {
	if Check(nil).Proceed() {
		t.Error("an empty round must block")
	}
	if Check([]findings.LaneReport{{Lane: "L2 caps"}}).Proceed() {
		t.Error("a round with no findings AND no reverified must block")
	}
}

// The escalation path leonard can't exercise (it's all-LOW): assert the
// classification AND that HIGH/UNTRIAGED actually render into the commit
// message and summary.
func TestEscalationRendering(t *testing.T) {
	reports := []findings.LaneReport{{
		Lane: "L2 caps", Slug: "caps",
		Reverified: []string{"clean probe"},
		Findings: []findings.Finding{
			{Severity: findings.High, Title: "DoS via X"},
			{Severity: findings.Untriaged, Title: "weird thing"},
			{Severity: findings.Low, Title: "minor"},
		},
	}}
	v := Check(reports)
	if !v.Proceed() {
		t.Fatalf("a complete round should proceed despite escalations: %v", v.Blockers)
	}
	if len(v.Escalations) != 2 {
		t.Fatalf("want 2 escalations (HIGH + UNTRIAGED), got %v", v.Escalations)
	}

	msg := CommitMessage(13, "leonard", reports, v)
	if !strings.Contains(msg, "REVIEW NEEDED") || !strings.Contains(msg, "HIGH") || !strings.Contains(msg, "UNTRIAGED") {
		t.Errorf("commit message must surface escalations:\n%s", msg)
	}

	sum := Summary(13, "leonard", "audit/bughunt-13", reports, v)
	if !strings.Contains(sum, "These need you") || !strings.Contains(sum, "HIGH") || !strings.Contains(sum, "UNTRIAGED") {
		t.Errorf("summary must list escalations:\n%s", sum)
	}
}

func TestSummaryBlockedPath(t *testing.T) {
	v := Check([]findings.LaneReport{{Lane: "L6 protocol", Failed: []string{"boom"}}})
	sum := Summary(13, "leonard", "", nil, v)
	if !strings.Contains(sum, "BLOCKED") || !strings.Contains(sum, "boom") {
		t.Errorf("blocked summary should say BLOCKED + the reason:\n%s", sum)
	}
}

func TestCleanRoundNoEscalations(t *testing.T) {
	// leonard's real shape: all-LOW, complete -> proceed, no escalations.
	reports := []findings.LaneReport{{
		Lane: "L2 caps", Reverified: []string{"a", "b"},
		Findings: []findings.Finding{{Severity: findings.Low, Title: "leak"}},
	}}
	v := Check(reports)
	if !v.Proceed() || len(v.Escalations) != 0 {
		t.Errorf("all-LOW complete round should proceed with no escalations: %+v", v)
	}
	if !strings.Contains(Summary(1, "leonard", "audit/bughunt-1", reports, v), "No escalations") {
		t.Error("summary should note no escalations")
	}
}
