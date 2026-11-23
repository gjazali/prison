package applecontainer

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAnchorEnsureCreatesMissingAnchor(t *testing.T) {
	wantRun := "container run --detach --name prison-anchor " +
		"--network default --cpus 1 --memory 200M " +
		"docker.io/library/alpine:latest sleep infinity"
	runner := newFixtureRunner()
	runner.answer(wantRun, fixtureResponse{})
	runner.answer("container exec prison-anchor true", fixtureResponse{})

	if err := newTestDriver(runner).Anchor().Ensure(context.Background()); err != nil {
		t.Fatalf("Anchor.Ensure: %v", err)
	}
	wantCalls := []string{
		"container inspect prison-anchor",
		wantRun,
		"container exec prison-anchor true",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("calls =\n%s\nwant\n%s",
			strings.Join(runner.calls, "\n"),
			strings.Join(wantCalls, "\n"))
	}
}

func TestAnchorEnsureLeavesRunningAnchorAlone(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container inspect prison-anchor", fixtureResponse{
		stdout: `[{"id":"prison-anchor","configuration":{"networks":[{"network":"default"}]},"status":{"state":"running","networks":[{"network":"default","ipv4Address":"192.168.64.2/24"}]}}]`,
	})

	if err := newTestDriver(runner).Anchor().Ensure(context.Background()); err != nil {
		t.Fatalf("Anchor.Ensure: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "container inspect prison-anchor" {
		t.Errorf("calls = %v, want only inspect", runner.calls)
	}
}

func TestAnchorEnsureStartsStoppedAnchor(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container inspect prison-anchor", fixtureResponse{
		stdout: `[{"id":"prison-anchor","configuration":{"networks":[{"network":"default"}]},"status":{"state":"stopped"}}]`,
	})
	runner.answer("container start prison-anchor", fixtureResponse{})
	runner.answer("container exec prison-anchor true", fixtureResponse{})

	if err := newTestDriver(runner).Anchor().Ensure(context.Background()); err != nil {
		t.Fatalf("Anchor.Ensure: %v", err)
	}
	wantCalls := []string{
		"container inspect prison-anchor",
		"container start prison-anchor",
		"container exec prison-anchor true",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("calls =\n%s\nwant\n%s",
			strings.Join(runner.calls, "\n"),
			strings.Join(wantCalls, "\n"))
	}
}

func TestAnchorEnsureRecreatesAnchorOnWrongNetwork(t *testing.T) {
	wantRun := "container run --detach --name prison-anchor " +
		"--network default --cpus 1 --memory 200M " +
		"docker.io/library/alpine:latest sleep infinity"
	runner := newFixtureRunner()
	runner.answer("container inspect prison-anchor", fixtureResponse{
		stdout: `[{"id":"prison-anchor","configuration":{"networks":[{"network":"prison"}]},"status":{"state":"running"}}]`,
	})
	runner.answer("container stop prison-anchor", fixtureResponse{})
	runner.answer("container delete prison-anchor", fixtureResponse{})
	runner.answer(wantRun, fixtureResponse{})
	runner.answer("container exec prison-anchor true", fixtureResponse{})

	if err := newTestDriver(runner).Anchor().Ensure(context.Background()); err != nil {
		t.Fatalf("Anchor.Ensure: %v", err)
	}
	wantCalls := []string{
		"container inspect prison-anchor",
		"container stop prison-anchor",
		"container delete prison-anchor",
		wantRun,
		"container exec prison-anchor true",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("calls =\n%s\nwant\n%s",
			strings.Join(runner.calls, "\n"),
			strings.Join(wantCalls, "\n"))
	}
}

func TestAnchorRemoveStopsAndDeletes(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		runner := newFixtureRunner()
		runner.answer("container inspect prison-anchor", fixtureResponse{
			stdout: `[{"id":"prison-anchor","configuration":{"networks":[{"network":"default"}]},"status":{"state":"running"}}]`,
		})
		runner.answer("container stop prison-anchor", fixtureResponse{})
		runner.answer("container delete prison-anchor", fixtureResponse{})

		if err := newTestDriver(runner).Anchor().Remove(context.Background()); err != nil {
			t.Fatalf("Anchor.Remove: %v", err)
		}
		wantCalls := []string{
			"container inspect prison-anchor",
			"container stop prison-anchor",
			"container delete prison-anchor",
		}
		if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
			t.Errorf("calls =\n%s\nwant\n%s",
				strings.Join(runner.calls, "\n"),
				strings.Join(wantCalls, "\n"))
		}
	})

	t.Run("absent", func(t *testing.T) {
		runner := newFixtureRunner()
		if err := newTestDriver(runner).Anchor().Remove(context.Background()); err != nil {
			t.Fatalf("Anchor.Remove: %v", err)
		}
		if len(runner.calls) != 1 || runner.calls[0] != "container inspect prison-anchor" {
			t.Errorf("calls = %v, want only inspect", runner.calls)
		}
	})
}

func TestDoctorReportsAnchorStates(t *testing.T) {
	cases := []struct {
		name    string
		inspect string
		want    string
	}{
		{
			name: "absent",
			want: "absent (prison-anchor; run `prison up` or `prison host anchor`)",
		},
		{
			name:    "stopped",
			inspect: `[{"id":"prison-anchor","configuration":{"networks":[{"network":"default"}]},"status":{"state":"stopped"}}]`,
			want:    "stopped (prison-anchor; run `prison up` or `prison host anchor`)",
		},
		{
			name:    "running",
			inspect: `[{"id":"prison-anchor","configuration":{"networks":[{"network":"default"}]},"status":{"state":"running","networks":[{"network":"default","ipv4Address":"192.168.64.2/24"}]}}]`,
			want:    "running (prison-anchor on default, 192.168.64.2)",
		},
		{
			name:    "wrong network",
			inspect: `[{"id":"prison-anchor","configuration":{"networks":[{"network":"prison"}]},"status":{"state":"running"}}]`,
			want:    `running on "prison" instead of "default" (prison-anchor; run ` + "`prison host anchor`" + `)`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := newFixtureRunner()
			if testCase.inspect != "" {
				runner.answer("container inspect prison-anchor",
					fixtureResponse{stdout: testCase.inspect})
			}
			var report bytes.Buffer
			newTestDriver(runner).Doctor(context.Background(), &report)
			text := report.String()
			if !strings.Contains(text, "\n  anchor\n") {
				t.Fatalf("doctor output missing anchor heading:\n%s", text)
			}
			if !strings.Contains(text, testCase.want) {
				t.Errorf("doctor output missing %q:\n%s", testCase.want, text)
			}
		})
	}
}
