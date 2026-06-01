package k3srunner

import (
	"strings"
	"testing"
)

func TestRenderJob(t *testing.T) {
	y := RenderJob("columbo-r1-l6", "l6", "columbo:slim", "/examples/columbo-cluster.target.yaml")
	for _, want := range []string{
		"name: columbo-r1-l6",
		"image: columbo:slim",
		"imagePullPolicy: Never",  // no registry; image is pre-imported
		"backoffLimit: 0",         // a failed pod must not retry and muddy the logs
		"restartPolicy: Never",
		`args: ["audit", "lane", "l6", "--workdir", "/work", "/examples/columbo-cluster.target.yaml"]`,
	} {
		if !strings.Contains(y, want) {
			t.Errorf("rendered Job missing %q:\n%s", want, y)
		}
	}
}
