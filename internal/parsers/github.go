package parsers

import (
	"os"

	"github.com/sanix-darker/git-ci/pkg/types"
	"gopkg.in/yaml.v3"
)

type GithubParser struct {}

type GithubWorkflow struct {
    Name string                 `yaml:"name"`
    Jobs map[string]*GithubJob  `yaml:"jobs"`
}

type GithubJob struct {
    RunsOn  string              `yaml:"runs-on"`
    Steps   []GithubStep        `yaml:"steps"`
    Env     map[string]string   `yaml:"env"`
}

type GithubStep struct {
    Name string `yaml:"name"`
    Run string `yaml:"run"`
    Uses string `yaml:"uses"`
}


func (p *GithubParser) Parse(ciFilePath string) (*types.Pipeline, error) {
    // read and unmarshall the file
    data, err := os.ReadFile(ciFilePath)
    if err != nil {
        return nil, err
    }

    var workflow GithubWorkflow
    if err := yaml.Unmarshal(data, &workflow); err != nil {
        return nil, err
    }

    pipeline := &types.Pipeline{
       Name: workflow.Name,
       Jobs: make(map[string]*types.Job),
    }

    for jobName, ghJob := range workflow.Jobs {
        job := &types.Job{
            Name: jobName,
            RunsOn: ghJob.RunsOn,
            Environment: ghJob.Env,
            Steps: make([]types.Step, len(ghJob.Steps)),
        }

        for i, ghStep := range ghJob.Steps {
            job.Steps[i] = types.Step{
               Name: ghStep.Name,
               Run: ghStep.Run,
               Uses: ghStep.Uses,
            }
        }

        pipeline.Jobs[jobName] = job
    }

    return pipeline, nil
}
