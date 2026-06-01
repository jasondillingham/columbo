// Package k3srunner runs a lane as a k3s Job and collects its findings over
// pod logs. It shells out to kubectl (no client-go dependency for v0.6). The
// Job-lifecycle handling is the load-bearing part: a crashed pod must surface
// its logs, not hang a `wait --for=complete` until timeout, so we poll for
// complete OR failed and set backoffLimit:0 so a failed pod is not retried.
package k3srunner

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/lanewire"
)

// RenderJob returns the Job manifest YAML for one lane.
func RenderJob(name, laneID, image, targetPath string) string {
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  labels:
    app: columbo
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 600
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: lane
          image: %s
          imagePullPolicy: Never
          args: ["audit", "lane", "%s", "--workdir", "/work", "%s"]
`, name, image, laneID, targetPath)
}

// RunLane applies the Job, waits for it to finish, and returns the lane's
// report. A failure (apply error, job failed, missing findings block) is
// returned as a LaneReport whose Failed field carries the reason + log tail, so
// a broken cluster lane shows up honestly in the round rather than vanishing.
func RunLane(round int, laneID, display, slug, image, targetPath string, timeout time.Duration) findings.LaneReport {
	name := fmt.Sprintf("columbo-r%d-%s", round, laneID)
	fail := func(msg string) findings.LaneReport {
		return findings.LaneReport{Lane: display, Slug: slug, Failed: []string{msg}}
	}

	_, _ = kubectl("delete", "job", name, "--ignore-not-found")
	if out, err := kubectlStdin(RenderJob(name, laneID, image, targetPath), "apply", "-f", "-"); err != nil {
		return fail("kubectl apply: " + err.Error() + " " + out)
	}

	status := waitJob(name, timeout)
	logs, _ := kubectl("logs", "job/"+name)
	switch status {
	case "complete":
		lr, err := lanewire.Extract(logs)
		if err != nil {
			return fail("findings: " + err.Error() + "\n" + lastLines(logs, 10))
		}
		// Trust the lane's identity from the orchestrator, not the pod.
		lr.Lane, lr.Slug = display, slug
		return lr
	case "failed":
		return fail("job failed:\n" + lastLines(logs, 15))
	default:
		return fail("job timed out after " + timeout.String() + ":\n" + lastLines(logs, 15))
	}
}

// waitJob polls for a terminal Job condition: "complete", "failed", or
// "timeout".
func waitJob(name string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s, _ := kubectl("get", "job", name, "-o", "jsonpath={.status.succeeded}"); strings.TrimSpace(s) == "1" {
			return "complete"
		}
		if f, _ := kubectl("get", "job", name, "-o", "jsonpath={.status.failed}"); strings.TrimSpace(f) != "" && strings.TrimSpace(f) != "0" {
			return "failed"
		}
		time.Sleep(2 * time.Second)
	}
	return "timeout"
}

func kubectl(args ...string) (string, error) {
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	return string(out), err
}

func kubectlStdin(stdin string, args ...string) (string, error) {
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
