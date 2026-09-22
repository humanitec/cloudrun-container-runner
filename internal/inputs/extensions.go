package inputs

type GoogleCloudRunExtensions struct {
	Pod     map[string]interface{} `json:"pod"`
	Service map[string]interface{} `json:"service"`
}

type KubernetesExtensions struct {
	Pod        map[string]interface{} `json:"pod"`
	Deployment map[string]interface{} `json:"deployment"`
	Job        map[string]interface{} `json:"job"`
}
type Extensions struct {
	GoogleCloudRun *GoogleCloudRunExtensions `json:"google-cloud-run"`
	Kubernetes     *KubernetesExtensions     `json:"kubernetes"`
}
