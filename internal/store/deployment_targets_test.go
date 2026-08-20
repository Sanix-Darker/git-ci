package store

import "testing"

func TestNormalizeEnqueueJobDeployment(t *testing.T) {
	tests := []struct {
		name        string
		job         EnqueueJob
		wantEnv     string
		wantTier    string
		wantFailure bool
	}{
		{name: "no target", job: EnqueueJob{}},
		{name: "default tier", job: EnqueueJob{EnvironmentName: " production "}, wantEnv: "production", wantTier: "other"},
		{name: "normalized tier", job: EnqueueJob{EnvironmentName: "preview", DeploymentTier: " STAGING "}, wantEnv: "preview", wantTier: "staging"},
		{name: "tier requires environment", job: EnqueueJob{DeploymentTier: "production"}, wantFailure: true},
		{name: "invalid tier", job: EnqueueJob{EnvironmentName: "production", DeploymentTier: "critical"}, wantFailure: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := test.job
			err := normalizeEnqueueJobDeployment(&job)
			if test.wantFailure {
				if err == nil {
					t.Fatal("normalization unexpectedly succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize deployment target: %v", err)
			}
			if job.EnvironmentName != test.wantEnv || job.DeploymentTier != test.wantTier {
				t.Fatalf("normalized job = %#v, want environment %q and tier %q", job, test.wantEnv, test.wantTier)
			}
		})
	}
}
