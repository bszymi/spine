// Command pincheck is Spine's supply-chain pin and toolchain-parity guard
// (INIT-024 / EPIC-004 / TASK-009). CI runs it in the toolchain-parity job;
// run it locally from the repository root:
//
//	go run ./dev/pincheck            # static rules only (no network)
//	go run ./dev/pincheck -resolve   # also read the golang digest's config from the registry
//
// It exits 1 and prints every refusal when go.mod has no release `toolchain`
// line, a golang image reference is not golang:<toolchain>-<variant>@sha256:
// <digest> (or differs from another one), an image is not pinned by digest,
// or a workflow action is not a full commit SHA with a `# vX.Y.Z` comment.
// See pincheck.go for why each rule exists.
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
	resolve := flag.Bool("resolve", false, "also read each golang digest's image config from the registry (docker buildx imagetools inspect; nothing is pulled) and require GOLANG_VERSION to equal the toolchain")
	flag.Parse()

	res, problems := check(*root)
	var report []string
	if *resolve && len(problems) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		report, problems = resolveGolang(ctx, res, dockerInspect)
		cancel()
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
		res.toolchain, len(res.golang), res.images, res.actions)
}

// dockerInspect reads the manifest and image config of ref from its
// registry. imagetools never pulls layers and never touches the local image
// store, so a check run cannot change what a later build uses.
func dockerInspect(ctx context.Context, ref string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", "--format", "{{json .Image}}", ref)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}
