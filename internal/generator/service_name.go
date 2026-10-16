package generator

import (
	"cmp"
	"maps"

	"github.com/score-spec/score-go/types"
)

// ServiceName returns the Cloud Run service name for the workload:
// the ServiceNameAnnotation if set, metadata.name otherwise.
func ServiceName(spec *types.Workload) string {
	annotations, _ := spec.Metadata["annotations"].(map[string]any)
	name, _ := annotations[ServiceNameAnnotation].(string)
	workloadName, _ := spec.Metadata["name"].(string)
	return cmp.Or(name, workloadName)
}

// WithServiceName returns a copy of the workload with the ServiceNameAnnotation set to name.
func WithServiceName(spec types.Workload, name string) types.Workload {
	metadata := maps.Clone(spec.Metadata)
	if metadata == nil {
		metadata = types.WorkloadMetadata{}
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	annotations = maps.Clone(annotations)
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations[ServiceNameAnnotation] = name
	metadata["annotations"] = annotations
	spec.Metadata = metadata
	return spec
}
