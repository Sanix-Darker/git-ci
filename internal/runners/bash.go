package runners

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/sanix-darker/git-ci/pkg/types"
)

type BashRunner struct{}

func (r *BashRunner) RunJob(job *types.Job, workdir string) error {
    fmt.Printf("\n Running job: %s\n", job.Name)
    fmt.Printf("Working directory: %s\n\n", workdir)

    for i, step := range job.Steps {
        if step.Uses != "" {
            fmt.Printf("  [%d/%d] Skipping action: %s (actions not supported yet)\n",
                i+1, len(job.Steps), step.Name)
            continue
        }

        if step.Run == "" {
            continue
        }

        fmt.Printf("[%d/%d] ▶️ %s\n", i+1, len(job.Steps), step.Name)

        cmd := exec.Command("bash", "-c", step.Run)
        cmd.Dir = workdir
        cmd.Stdout = os.Stdout
        cmd.Stderr = os.Stderr
        cmd.Env = os.Environ()

        for k, v := range job.Environment {
            cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
        }

        if err := cmd.Run(); err != nil {
            return fmt.Errorf("step '%s' failed: %w", step.Name, err)
        }
    }

    fmt.Println("\n Job completed successfully!")
    return nil
}
