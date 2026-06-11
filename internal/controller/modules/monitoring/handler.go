package monitoring

import (
	operatorv1 "github.com/openshift/api/operator/v1"

	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	serviceApi "github.com/opendatahub-io/opendatahub-operator/v2/api/services/v1alpha1"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
)

const (
	moduleName = serviceApi.MonitoringServiceName
	crName     = serviceApi.MonitoringInstanceName
)

type handler struct {
	modules.BaseHandler
}

func NewHandler() *handler {
	return &handler{
		BaseHandler: modules.BaseHandler{
			Config: modules.ModuleConfig{
				Name:              moduleName,
				CRName:            crName,
				ReleaseName:       "odh-observability",
				ChartDir:          "odh-observability",
				NamespaceValueKey: "operatorNamespace",
				GVK:               gvk.Monitoring,
				RelatedImages: []string{
					"RELATED_IMAGE_ODH_KUBE_RBAC_PROXY_IMAGE",
					"RELATED_IMAGE_OSE_PROM_LABEL_PROXY_IMAGE",
					"RELATED_IMAGE_CLI_IMAGE",
					"RELATED_IMAGE_PERSES_IMAGE",
				},
				IsEnabledFn:  isEnabled,
				SpecAccessor: specAccessor,
			},
		},
	}
}

func isEnabled(platform *modules.PlatformContext) bool {
	if platform == nil {
		return false
	}
	if platform.DSCI != nil {
		return platform.DSCI.Spec.Monitoring.ManagementState == operatorv1.Managed
	}
	if platform.Platform != nil {
		return platform.Platform.Spec.Modules.Monitoring.ManagementState == operatorv1.Managed
	}
	return false
}

func specAccessor(platform *modules.PlatformContext) any {
	if platform.DSCI != nil {
		return &platform.DSCI.Spec.Monitoring
	}
	if platform.Platform != nil {
		return &common.ManagementSpec{
			ManagementState: platform.Platform.Spec.Modules.Monitoring.ManagementState,
		}
	}
	return nil
}
