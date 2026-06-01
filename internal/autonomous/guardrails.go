// Package autonomous drives a hands-off audit round: run lanes, apply
// guardrails, and leave a PR-ready local audit branch. The guardrails are the
// point — they replace the human attention a Driven round gets for free
// (DESIGN): when to stop (block), and when a call needs a human (escalate).
package autonomous

import (
	"fmt"
	"strings"

	"github.com/jasondillingham/columbo/internal/findings"
)

// Verdict is the guardrail decision for a round.
type Verdict struct {
	// Blockers are reasons NOT to finalize: the audit is incomplete or unsafe
	// to commit, so producing a "done" branch would misrepresent coverage.
	Blockers []string
	// Escalations are findings a human must rule on (severity calls,
	// HIGH/CRITICAL sign-off). They do NOT block — the human reviews the
	// branch — but they are surfaced in the summary and commit message.
	Escalations []string
}

// Proceed reports whether the round may be committed (no blockers).
func (v Verdict) Proceed() bool { return len(v.Blockers) == 0 }

// Check applies the round-level guardrails. Pure: it reads only the reports.
// Dirty-tree (a git op) is added as a blocker by the command layer.
func Check(reports []findings.LaneReport) Verdict {
	var v Verdict

	total := 0
	for _, r := range reports {
		total += len(r.Findings) + len(r.Reverified)
		// A failed lane is not "0 findings" — it is "unknown coverage".
		for _, f := range r.Failed {
			v.Blockers = append(v.Blockers, fmt.Sprintf("lane %q failed to run: %s", r.Lane, oneLine(f)))
		}
	}
	if len(reports) == 0 || total == 0 {
		v.Blockers = append(v.Blockers, "no lane produced any result (empty round)")
	}

	// Escalations: severities a human must own. UNTRIAGED = the probe could not
	// justify a severity; HIGH/CRITICAL = needs sign-off before acting.
	counts := map[findings.Severity]int{}
	for _, r := range reports {
		for _, f := range r.Findings {
			switch f.Severity {
			case findings.Untriaged, findings.High, findings.Critical:
				counts[f.Severity]++
			}
		}
	}
	for _, sev := range []findings.Severity{findings.Critical, findings.High, findings.Untriaged} {
		if n := counts[sev]; n > 0 {
			reason := "needs human sign-off"
			if sev == findings.Untriaged {
				reason = "needs a human severity call"
			}
			v.Escalations = append(v.Escalations, fmt.Sprintf("%d %s finding(s) — %s", n, sev, reason))
		}
	}
	return v
}

// CommitMessage builds the audit-branch commit message, leading with any
// review-needed escalations so a PR opens with them front and center.
func CommitMessage(round int, target string, reports []findings.LaneReport, v Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "audit: bughunt-%d for %s\n\n", round, target)
	if len(v.Escalations) > 0 {
		b.WriteString("REVIEW NEEDED:\n")
		for _, e := range v.Escalations {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		b.WriteString("\n")
	}
	b.WriteString("Lanes:\n")
	for _, r := range reports {
		fmt.Fprintf(&b, "- %s: %d finding(s), %d reverified%s\n",
			r.Lane, len(r.Findings), len(r.Reverified), failedNote(r))
	}
	return b.String()
}

// Summary is the wake-up text the operator reads first.
func Summary(round int, target, branch string, reports []findings.LaneReport, v Verdict) string {
	var b strings.Builder
	if !v.Proceed() {
		fmt.Fprintf(&b, "BLOCKED: bughunt-%d for %s was not committed.\n", round, target)
		for _, bl := range v.Blockers {
			fmt.Fprintf(&b, "  - %s\n", bl)
		}
		b.WriteString("Fix the above and re-run.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "bughunt-%d for %s committed to branch %s.\n", round, target, branch)
	sev := map[findings.Severity]int{}
	tf := 0
	for _, r := range reports {
		for _, f := range r.Findings {
			sev[f.Severity]++
			tf++
		}
	}
	fmt.Fprintf(&b, "Findings: %d (CRITICAL %d, HIGH %d, MEDIUM %d, LOW %d, UNTRIAGED %d)\n",
		tf, sev[findings.Critical], sev[findings.High], sev[findings.Medium], sev[findings.Low], sev[findings.Untriaged])
	if len(v.Escalations) > 0 {
		b.WriteString("These need you:\n")
		for _, e := range v.Escalations {
			fmt.Fprintf(&b, "  - %s\n", e)
		}
	} else {
		b.WriteString("No escalations. Review and open a PR when ready.\n")
	}
	return b.String()
}

func failedNote(r findings.LaneReport) string {
	if len(r.Failed) > 0 {
		return fmt.Sprintf(", %d FAILED", len(r.Failed))
	}
	return ""
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
