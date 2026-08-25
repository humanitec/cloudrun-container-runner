package google

import (
	"fmt"
	"os"
)

const (
	EnvProject = "CLOUDSDK_CORE_PROJECT"
	EnvRegion  = "CLOUDSDK_RUN_REGION"
)

// Target is the Google Cloud project and region a deployment goes to.
type Target struct {
	Project string
	Region  string
}

// TargetFromEnv reads the deployment target from environment variables.
func TargetFromEnv() Target {
	return Target{
		Project: os.Getenv(EnvProject),
		Region:  os.Getenv(EnvRegion),
	}
}

// Validate reports whether the target is complete, naming the environment
// variable that supplies whichever half is missing.
func (t Target) Validate() error {
	if t.Project == "" {
		return fmt.Errorf("%s is not set", EnvProject)
	}
	if t.Region == "" {
		return fmt.Errorf("%s is not set", EnvRegion)
	}
	return nil
}
