package cloudrun

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/score"
)

// fakeSecretSaver stands in for Google Secret Manager, recording what would be
// written and handing back a fixed version, so that conversion can be exercised
// without Google credentials.
type fakeSecretSaver struct {
	saved   map[string]string
	version string

	// err fails every call, as Secret Manager does when the deployer lacks the
	// grant or the region is wrong.
	err error
}

func newFakeSecretSaver() *fakeSecretSaver {
	return &fakeSecretSaver{saved: map[string]string{}, version: "1"}
}

func (f *fakeSecretSaver) SaveSecret(_ context.Context, name, value string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.saved[name] = value
	return f.version, nil
}

// driverInputs is the shape the Container Driver sends, as much of it as the
// converter needs. The converter itself does not know about it, but the
// fixtures below are easier to read as the JSON that actually arrives.
type driverInputs struct {
	Id            string                    `json:"id"`
	Spec          types.Workload            `json:"spec"`
	Substitutions map[string]score.SubValue `json:"substitutions,omitempty"`
	Extensions    score.Extensions          `json:"extensions,omitempty"`
}

// optionsFrom turns the Driver's JSON into what FromScoreWorkload takes.
func optionsFrom(t *testing.T, inputsJSON, serviceAccount string) Options {
	t.Helper()

	var in driverInputs
	require.NoError(t, json.Unmarshal([]byte(inputsJSON), &in))

	return Options{
		Name:           in.Id,
		Workload:       &in.Spec,
		Substitutions:  in.Substitutions,
		Extension:      in.Extensions.GoogleCloudRun,
		ServiceAccount: serviceAccount,
	}
}

func TestFromScoreWorkload(t *testing.T) {
	const serviceAccount = "cloudrun-runtime@my-gcp-project.iam.gserviceaccount.com"

	saver := newFakeSecretSaver()
	saver.version = "7"

	// Note: we use check a simple `extensions` case here, for more sophisticated cases see TestFromScoreWorkload_Extensions.
	inputsJSON := `{
	  "id": "hello-world-dev",
	  "spec": {
	    "apiVersion": "score.dev/v1b1",
	    "metadata": {"name": "hello-world"},
	    "containers": {
	      "main": {
	        "image": "busybox:latest",
	        "variables": {
	          "DB_HOST": "${resources.db.host}",
	          "DB_PASSWORD": "${resources.db.password}",
	          "DB_URL": "postgres://${resources.db.password}@${resources.db.host}/db"
            },
	        "volumes": {
	          "/app/backend/data": {"source": "${resources.data}", "path": "sub/dir", "readOnly": true}
	        },
	        "files": {
	          "/etc/app/config.yaml": {"content": "${resources.db.password}", "mode": "0400"}
	        }
	      }
	    },
	    "resources": {
          "db": {"type": "postgres"},
          "data": {"type": "volume"}
        }
	  },
	  "substitutions": {
        "resources.db.password": {"secret": {"type": "direct", "value": "s3cr3t"}},
        "resources.db.host": {"value": "db.example.com"},
        "resources.data": {
	      "value": {
	        "kubernetes": {"persistentVolumeClaim": {"claimName": "data"}},
	        "google-cloud-run": {
	          "csi": {
	            "driver": "gcsfuse.run.googleapis.com",
	            "readOnly": true,
	            "volumeAttributes": {"bucketName": "my-bucket-name"}
	          }
	        }
	      }
	    }
      },
      "extensions": {
		"google-cloud-run": {
		  "pod": {
		    "metadata": {"annotations": {"autoscaling.knative.dev/maxScale": "5"}}
          }
        }
      }
	}`
	service, err := FromScoreWorkload(t.Context(), optionsFrom(t, inputsJSON, serviceAccount), saver)
	require.NoError(t, err)
	require.NotNil(t, service)

	wantNameSingle := secretmanager.SecretName("hello-world-dev", "main", "env", "DB_PASSWORD")
	wantNameCombined := secretmanager.SecretName("hello-world-dev", "main", "env", "DB_URL")
	wantNameFile := secretmanager.SecretName("hello-world-dev", "main", "vol", "config.yaml")

	// Check container env
	require.Len(t, service.Spec.Template.Spec.Containers, 1)
	env := service.Spec.Template.Spec.Containers[0].Env
	require.Len(t, env, 3)

	assert.Equal(t, "DB_HOST", env[0].Name)
	assert.Equal(t, "db.example.com", env[0].Value)

	assert.Equal(t, "DB_PASSWORD", env[1].Name)
	assert.Empty(t, env[1].Value, "the secret must not be inlined into the manifest")
	require.NotNil(t, env[1].ValueFrom)
	require.NotNil(t, env[1].ValueFrom.SecretKeyRef)
	assert.Equal(t, wantNameSingle, env[1].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "7", env[1].ValueFrom.SecretKeyRef.Key, "the manifest pins the version that was just written")

	assert.Equal(t, "DB_URL", env[2].Name)
	assert.Empty(t, env[2].Value, "the secret must not be inlined into the manifest")
	require.NotNil(t, env[2].ValueFrom)
	require.NotNil(t, env[2].ValueFrom.SecretKeyRef)
	assert.Equal(t, wantNameCombined, env[2].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "7", env[2].ValueFrom.SecretKeyRef.Key, "the manifest pins the version that was just written")

	// Check volumes and mounts
	volumes := service.Spec.Template.Spec.Volumes
	require.Len(t, volumes, 2)
	mounts := service.Spec.Template.Spec.Containers[0].VolumeMounts
	require.Len(t, mounts, 2)

	// From `files`
	require.NotNil(t, volumes[0].Secret)
	assert.Equal(t, wantNameFile, volumes[0].Secret.SecretName)
	require.Len(t, volumes[0].Secret.Items, 1)
	assert.Equal(t, "7", volumes[0].Secret.Items[0].Key, "the manifest pins the version that was just written")
	assert.Equal(t, "config.yaml", volumes[0].Secret.Items[0].Path)
	assert.Equal(t, int64(0o400), volumes[0].Secret.Items[0].Mode)

	assert.Equal(t, "/etc/app", mounts[0].MountPath)
	assert.Equal(t, volumes[0].Name, mounts[0].Name)

	// From `volumes`
	require.NotNil(t, volumes[1].Csi)
	assert.Equal(t, "resources-data", volumes[1].Name)
	assert.Equal(t, "gcsfuse.run.googleapis.com", volumes[1].Csi.Driver)
	assert.True(t, volumes[1].Csi.ReadOnly)
	assert.Equal(t, map[string]string{"bucketName": "my-bucket-name"}, volumes[1].Csi.VolumeAttributes)

	assert.Equal(t, "resources-data", mounts[1].Name)
	assert.Equal(t, "/app/backend/data", mounts[1].MountPath)
	assert.Equal(t, "sub/dir", mounts[1].SubPath)
	assert.True(t, mounts[1].ReadOnly)

	// Check stored secrets
	assert.Equal(t, map[string]string{
		wantNameSingle:   "s3cr3t",
		wantNameCombined: "postgres://s3cr3t@db.example.com/db",
		wantNameFile:     "s3cr3t",
	}, saver.saved)

	// Check service account
	assert.Equal(t, serviceAccount, service.Spec.Template.Spec.ServiceAccountName)

	// Check pod extension is applied
	assert.Equal(t, "5", service.Spec.Template.Metadata.Annotations["autoscaling.knative.dev/maxScale"])
}

func TestFromScoreWorkload_Failures(t *testing.T) {
	testCases := []struct {
		name        string
		saveErr     error
		inputsJSON  string
		wantErrText string
	}{
		{
			name: "no containers",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {}
			  }
			}`,
			wantErrText: "needs at least one container",
		},
		{
			// The Operator resolves secrets and sends flat values for now.
			// This may change in the future, but for now it's not supported here.
			name: "a variable holds a secret reference",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "variables": {"DB_PASSWORD": "${resources.db.password}"}
			      }
			    },
			    "resources": {"db": {"type": "postgres"}}
			  },
			  "substitutions": {
			    "resources.db.password": {
			      "secret": {"store": "gsm", "ref": "projects/1234567890/secrets/db/versions/3"}
			    }
			  }
			}`,
			wantErrText: "secret with name DB_PASSWORD: secret references are not supported",
		},
		{
			name: "a file holds a secret reference",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "files": {"/etc/app/config.yaml": {"content": "${resources.db.password}"}}
			      }
			    },
			    "resources": {"db": {"type": "postgres"}}
			  },
			  "substitutions": {
			    "resources.db.password": {
			      "secret": {"store": "gsm", "ref": "projects/1234567890/secrets/db/versions/3"}
			    }
			  }
			}`,
			wantErrText: "file /etc/app/config.yaml: secret references are not supported",
		},
		{
			name:    "secret manager refuses a variable's secret",
			saveErr: assert.AnError,
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "variables": {"DB_PASSWORD": "${resources.db.password}"}
			      }
			    },
			    "resources": {"db": {"type": "postgres"}}
			  },
			  "substitutions": {"resources.db.password": {"secret": {"type": "direct", "value": "s3cr3t"}}}
			}`,
			wantErrText: "resolving variable DB_PASSWORD in container main: saving secret to google secret manager",
		},
		{
			name:    "secret manager refuses a file's secret",
			saveErr: assert.AnError,
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "files": {"/etc/app/config.yaml": {"content": "${resources.db.password}"}}
			      }
			    },
			    "resources": {"db": {"type": "postgres"}}
			  },
			  "substitutions": {"resources.db.password": {"secret": {"type": "direct", "value": "s3cr3t"}}}
			}`,
			wantErrText: "file /etc/app/config.yaml: saving secret to google secret manager",
		},
		{
			// Cloud Run mounts one secret version per directory, so two files sharing a directory cannot both be mounted.
			name: "two files in one directory",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "files": {
			          "/etc/app/config.yaml": {"content": "${resources.db.password}"},
			          "/etc/app/extra.yaml": {"content": "${resources.db.password}"}
			        }
			      }
			    },
			    "resources": {"db": {"type": "postgres"}}
			  },
			  "substitutions": {"resources.db.password": {"secret": {"type": "direct", "value": "s3cr3t"}}}
			}`,
			wantErrText: "cloudrun only supports mounting 1 file per directory",
		},
		{
			// Every mounted file becomes a Secret Manager secret, Cloud Run has no ConfigMap equivalent.
			name: "a file's content is not a secret",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "files": {"/etc/app/config.yaml": {"content": "log_level: debug"}}
			      }
			    }
			  }
			}`,
			wantErrText: "file /etc/app/config.yaml: cloudrun only supports mounting files from google secret manager secrets (gsm)",
		},
		{
			name: "a volume has no cloud run spec",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "volumes": {"/app/data": {"source": "${resources.data}"}}
			      }
			    },
			    "resources": {"data": {"type": "volume"}}
			  },
			  "substitutions": {
			    "resources.data": {
			      "value": {"kubernetes": {"persistentVolumeClaim": {"claimName": "data"}}}
			    }
			  }
			}`,
			wantErrText: "resolving volume resource resources.data: platform google-cloud-run does not exist in output or is not an object",
		},
		{
			name: "a volume source is not a placeholder",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "volumes": {"/app/data": {"source": "my-bucket-name"}}
			      }
			    },
			    "resources": {"data": {"type": "volume"}}
			  }
			}`,
			wantErrText: `source must be a placeholder referencing a volume, got "my-bucket-name"`,
		},
		{
			name: "a volume references a resource that is not a volume",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "volumes": {"/app/data": {"source": "${resources.db}"}}
			      }
			    },
			    "resources": {"db": {"type": "postgres"}}
			  }
			}`,
			wantErrText: "placeholder ${resources.db} in source is not of type volume, got postgres",
		},
		{
			name: "a volume references an undeclared resource",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {
			        "image": "busybox:latest",
			        "volumes": {"/app/data": {"source": "${resources.data}"}}
			      }
			    }
			  }
			}`,
			wantErrText: "placeholder ${resources.data} cannot be resolved: no resource with name data",
		},
		{
			name: "the service has two ports",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {"main": {"image": "busybox:latest"}},
			    "service": {
			      "ports": {
			        "http": {"port": 8080},
			        "metrics": {"port": 9090}
			      }
			    }
			  }
			}`,
			wantErrText: "cloudrun only supports a single port, got 2 ports",
		},
		{
			name: "two containers behind one port",
			inputsJSON: `{
			  "id": "hello-world-dev",
			  "spec": {
			    "apiVersion": "score.dev/v1b1",
			    "metadata": {"name": "hello-world"},
			    "containers": {
			      "main": {"image": "busybox:latest"},
			      "sidecar": {"image": "busybox:latest"}
			    },
			    "service": {"ports": {"http": {"port": 8080}}}
			  }
			}`,
			wantErrText: "cloudrun only supports ingress on a single container",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			saver := newFakeSecretSaver()
			saver.err = testCase.saveErr

			service, err := FromScoreWorkload(t.Context(), optionsFrom(t, testCase.inputsJSON, ""), saver)

			require.ErrorContains(t, err, testCase.wantErrText)
			// A manifest alongside an error is one a caller might deploy.
			assert.Nil(t, service)
			if testCase.saveErr != nil {
				assert.ErrorIs(t, err, testCase.saveErr, "the Secret Manager error has to survive wrapping")
			}
		})
	}
}

func TestFromScoreWorkload_Extensions(t *testing.T) {
	const spec = `"spec": {
	    "apiVersion": "score.dev/v1b1",
	    "metadata": {"name": "hello-world"},
	    "containers": {
	      "main": {"image": "busybox:latest", "variables": {"A": "1"}}
	    }
	  }`

	t.Run("no extensions", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`}`, "")
		require.Nil(t, opts.Extension)

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)
		require.Len(t, service.Spec.Template.Spec.Containers, 1)
		assert.Equal(t, "busybox:latest", service.Spec.Template.Spec.Containers[0].Image)
	})

	t.Run("kubernetes extensions are ignored", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"kubernetes": {"pod": {"spec": {"serviceAccountName": "k8s-sa"}}}}
		}`, "")
		require.Nil(t, opts.Extension)

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)
		assert.Empty(t, service.Spec.Template.Spec.ServiceAccountName)
	})

	t.Run("pod extension", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {"pod": {"spec": {
		    "containers": [{
		      "name": "main",
		      "env": [{"name": "B", "value": "2"}],
		      "resources": {"limits": {"cpu": "2", "memory": "1Gi"}}
		    }]
		  }}}}
		}`, "")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)

		require.Len(t, service.Spec.Template.Spec.Containers, 1)
		container := service.Spec.Template.Spec.Containers[0]
		assert.Equal(t, "busybox:latest", container.Image, "fields the patch does not name are kept")
		assert.Equal(t, map[string]string{"cpu": "2", "memory": "1Gi"}, container.Resources.Limits)

		envNames := make([]string, 0, len(container.Env))
		for _, e := range container.Env {
			envNames = append(envNames, e.Name)
		}
		assert.ElementsMatch(t, []string{"A", "B"}, envNames, "env merges by name")
	})

	t.Run("pod extension metadata lands on the revision template", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {"pod": {"metadata": {
		    "labels": {"team": "platform"},
		    "annotations": {"autoscaling.knative.dev/maxScale": "5"}
		  }}}}
		}`, "")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"team": "platform"}, service.Spec.Template.Metadata.Labels)
		assert.Equal(t, map[string]string{"autoscaling.knative.dev/maxScale": "5"}, service.Spec.Template.Metadata.Annotations)
		assert.Empty(t, service.Metadata.Labels, "pod metadata stays off the service")
	})

	t.Run("pod extension overrides the service account", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {"pod": {"spec": {"serviceAccountName": "override@x.iam.gserviceaccount.com"}}}}
		}`, "runtime@x.iam.gserviceaccount.com")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)
		assert.Equal(t, "override@x.iam.gserviceaccount.com", service.Spec.Template.Spec.ServiceAccountName)
	})

	t.Run("service extension", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {"service": {
		    "metadata": {
		      "labels": {"team": "platform"},
		      "annotations": {"run.googleapis.com/ingress": "internal"}
		    },
		    "spec": {"template": {
		      "metadata": {"annotations": {"autoscaling.knative.dev/maxScale": "5"}},
		      "spec": {"containerConcurrency": 10}
		    }}
		  }}}
		}`, "")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)

		assert.Equal(t, "hello-world-dev", service.Metadata.Name, "fields the patch does not name are kept")
		assert.Equal(t, map[string]string{"team": "platform"}, service.Metadata.Labels)
		assert.Equal(t, map[string]string{"run.googleapis.com/ingress": "internal"}, service.Metadata.Annotations)
		assert.Equal(t, map[string]string{"autoscaling.knative.dev/maxScale": "5"}, service.Spec.Template.Metadata.Annotations)
		assert.Equal(t, int64(10), service.Spec.Template.Spec.ContainerConcurrency)
		require.Len(t, service.Spec.Template.Spec.Containers, 1)
		assert.Equal(t, "busybox:latest", service.Spec.Template.Spec.Containers[0].Image)
	})

	t.Run("service extension merges containers by name", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {"service": {"spec": {"template": {"spec": {
		    "containers": [{"name": "main", "env": [{"name": "B", "value": "2"}]}]
		  }}}}}}
		}`, "")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)

		require.Len(t, service.Spec.Template.Spec.Containers, 1)
		container := service.Spec.Template.Spec.Containers[0]
		assert.Equal(t, "busybox:latest", container.Image)
		require.Len(t, container.Env, 2)
	})

	t.Run("pod and service extensions together", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {
		    "pod": {"spec": {"serviceAccountName": "override@x.iam.gserviceaccount.com"}},
		    "service": {"metadata": {"labels": {"team": "platform"}}}
		  }}
		}`, "")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.NoError(t, err)
		assert.Equal(t, "override@x.iam.gserviceaccount.com", service.Spec.Template.Spec.ServiceAccountName)
		assert.Equal(t, map[string]string{"team": "platform"}, service.Metadata.Labels)
	})

	t.Run("invalid pod extension", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {"pod": {"spec": "not-an-object"}}}
		}`, "")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.ErrorContains(t, err, "v1.Pod")
		assert.Nil(t, service)
	})

	t.Run("invalid service extension", func(t *testing.T) {
		opts := optionsFrom(t, `{"id": "hello-world-dev", `+spec+`,
		  "extensions": {"google-cloud-run": {"service": {"spec": "not-an-object"}}}
		}`, "")

		service, err := FromScoreWorkload(t.Context(), opts, newFakeSecretSaver())
		require.ErrorContains(t, err, "unable to unmarshal patched v1.Service")
		assert.Nil(t, service)
	})
}
