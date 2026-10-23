// Command pincheck is Spine's supply-chain pin and toolchain-parity guard
// (INIT-024 / EPIC-004 / TASK-009). CI runs it in the toolchain-parity job;
// run it locally from the repository root:
//
//	go run ./dev/pincheck            # static rules only (no network)
//	go run ./dev/pincheck -resolve   # also verify digests and action tags remotely
//
// It exits 1 and prints every refusal when go.mod has no release `toolchain`
// line; a golang image reference is not golang:<toolchain>-<variant>@sha256:
// <digest> (or differs from another one); an actions/setup-go step does not
// install exactly that toolchain; an image is not pinned by digest; a compose
// and a workflow image of one repository disagree; a job runs on a
// `*-latest` runner; or an action is not a full commit SHA with a
// `# vX.Y.Z` comment. See pincheck.go for why each rule exists.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	root := flag.String("root", ".", "repository root to check")
	resolve := flag.Bool("resolve", false, "also read each golang digest's image config from its registry (docker buildx imagetools inspect; nothing is pulled) and each action's version tag from its repository (git ls-remote), and require them to match")
	flag.Parse()

	res, problems := check(*root)
	var report []string
	if *resolve && len(problems) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		r1, p1 := resolveGolang(ctx, res, dockerInspect)
		r2, p2 := resolveActions(ctx, res, gitTagCommit)
		cancel()
		report = append(report, r1...)
		report = append(report, r2...)
		problems = append(problems, p1...)
		problems = append(problems, p2...)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "pincheck: "+p.String())
		}
		fmt.Fprintf(os.Stderr, "pincheck: %d problem(s)\n", len(problems))
		os.Exit(1)
	}
	for _, line := range report {
		fmt.Println("pincheck: " + line)
	}
	fmt.Printf("pincheck: OK — toolchain go%s; %d golang reference(s) on one digest; %d image(s) and %d action(s) pinned\n",
		res.toolchain, len(res.golang), res.images, len(res.actions))
}

// dockerInspect reads the manifest and image config of ref from its
// registry. imagetools never pulls layers and never touches the local image
// store.
func dockerInspect(ctx context.Context, ref string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", "--format", "{{json .Image}}", ref)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

// gitTagCommit lists refs/tags/<tag> (and its peeled ^{}) of
// https://github.com/<repo>. repo and tag come from the actionRef and
// versionComment patterns, so they carry no option or URL syntax. Prompts
// are disabled: a missing or private repository fails rather than waiting.
func gitTagCommit(ctx context.Context, repo, tag string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--", "https://github.com/"+repo, "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return parseLsRemote(out, tag), nil
}
