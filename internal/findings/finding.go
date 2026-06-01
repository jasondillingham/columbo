// Package findings is the audit-format read/write layer: it turns Finding
// records into the bughunt-N-*.md docs Leonard and bosun use, and parses the
// rollup table back (the writer's round-trip contract).
//
// The writer scaffolds, it does not author. A probe declares a Severity only
// when the class is characterized; otherwise the finding is UNTRIAGED and the
// operator fills it in. Columbo never guesses a severity. See docs/v0.3-plan.md.
package findings

// Severity is the audit severity scale (audits/README.md), plus UNTRIAGED for
// findings whose severity the probe could not justify.
type Severity string

const (
	Critical  Severity = "CRITICAL"
	High      Severity = "HIGH"
	Medium    Severity = "MEDIUM"
	Low       Severity = "LOW"
	Untriaged Severity = "UNTRIAGED"
)

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool {
	switch s {
	case Critical, High, Medium, Low, Untriaged:
		return true
	}
	return false
}

// Cell renders the severity for a rollup table cell: bold for HIGH/CRITICAL
// (matching the real audit format), plain otherwise.
func (s Severity) Cell() string {
	if s == High || s == Critical {
		return "**" + string(s) + "**"
	}
	return string(s)
}

// Finding is one recorded audit finding. The probe fills what it can capture
// mechanically (Title, Files, Reproducer, Observed, Expected, and Severity
// when the class is known); the writer fills ID and Discovered; the operator
// fills Severity when UNTRIAGED and FixShape when the probe could not size it.
type Finding struct {
	ID         string   // F001.. assigned by the writer, per round
	Severity   Severity // CRITICAL|HIGH|MEDIUM|LOW|UNTRIAGED
	Lane       string   // e.g. "L1 build invariants"
	Title      string   // one-line, goes in the rollup table
	Status     string   // confirmed|withdrawn|reference (default: confirmed)
	Files      []string // repo-relative paths into the TARGET
	Reproducer string   // runnable verbatim (captured by the probe)
	Observed   string   // what happened
	Expected   string   // the contract that was violated
	FixShape   string   // smallest change; empty -> writer emits a TODO(operator)
	Discovered string   // DESIGN's "date + lane" stamp, filled at write time

	// Class and Locus drive structural dedup (internal/reconcile). Class is a
	// stable key for "the same kind of finding" (probe-assigned, e.g.
	// "reflect-null-leak"); Locus names this instance's site (e.g.
	// "find_symbol.query"). Findings sharing a (Severity, Class) collapse into
	// one finding listing every Locus. Empty Class never merges.
	Class string
	Locus string
}
