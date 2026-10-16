package generator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/score-spec/score-go/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/score"
)

// Platform defines a target platform for which K8s objects are generated.
// Cloud Run is built on Knative and uses Kubernetes API specification, that's why we can use
// Kubernetes Score converter, but we need to specify the target platform as Cloud Run for specifics.
const Platform = "google-cloud-run"

// ServiceNameAnnotation is the workload annotation that names the Cloud Run service.
// Without it, the service is named after metadata.name.
const ServiceNameAnnotation = "google-cloud-run.score.dev/service-name"

// SecretSaver stores a secret value of a service under name and returns the full resource name of a secret version,
// so that it can be referenced in the Cloud Run service manifest.
type SecretSaver interface {
	SaveSecret(ctx context.Context, service, name, value string) (string, error)
}

type CloudRunGenerator struct {
	secrets SecretSaver
}

func NewCloudRunGenerator(secrets SecretSaver) score.Generator {
	return &CloudRunGenerator{
		secrets: secrets,
	}
}

// Generate generates a Cloud Run Service manifest from the score spec and the substitution map.
func (p *CloudRunGenerator) Generate(ctx context.Context, spec *types.Workload, substitutions map[string]score.SubValue) (map[string][]byte, error) {
	if spec == nil {
		return map[string][]byte{}, nil
	}

	serviceName := ServiceName(spec)

	builder := NewServiceBuilder(*spec, substitutions, p.secrets, serviceName)

	pod, err := builder.Pod(ctx)
	if err != nil {
		return nil, fmt.Errorf("generating cloud run pod: %w", err)
	}

	if spec.Service != nil {
		if len(spec.Service.Ports) > 1 {
			return nil, fmt.Errorf("cloudrun only supports a single port, got %d ports", len(spec.Service.Ports))
		}
		containerPorts := make([]corev1.ContainerPort, 0)
		for portName, port := range spec.Service.Ports {
			portNum := int32(port.Port)
			if port.TargetPort != nil {
				portNum = int32(*port.TargetPort)
			}
			containerPort := corev1.ContainerPort{
				ContainerPort: portNum,
			}
			if portName == "http1" || portName == "h2c" {
				containerPort.Name = portName
			}
			containerPorts = append(containerPorts, containerPort)
		}
		if len(spec.Containers) > 1 {
			return nil, fmt.Errorf("cloudrun only supports ingress on a single container: ambiguous container as there are %d containers", len(spec.Containers))
		}
		container := pod.Spec.Containers[0]
		container.Ports = containerPorts
		pod.Spec.Containers[0] = container
	}

	service := servingv1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "serving.knative.dev/v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: serviceName,
		},
		Spec: servingv1.ServiceSpec{
			ConfigurationSpec: servingv1.ConfigurationSpec{
				Template: servingv1.RevisionTemplateSpec{
					ObjectMeta: pod.ObjectMeta,
					Spec: servingv1.RevisionSpec{
						PodSpec: pod.Spec,
					},
				},
			},
		},
	}

	serviceAsBytes, err := json.Marshal(&service)
	if err != nil {
		return nil, fmt.Errorf("unable to serialize knative service to json: %w", err)
	}

	return map[string][]byte{
		"service": serviceAsBytes,
	}, nil
}
