package dscinitialization

import (
	"context"
	dsciv1 "github.com/opendatahub-io/opendatahub-operator/v2/api/dscinitialization/v1"
	serviceApi "github.com/opendatahub-io/opendatahub-operator/v2/api/services/v1alpha1"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/components/dashboard"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *DSCInitializationReconciler) createAuth(ctx context.Context, dscInit *dsciv1.DSCInitialization) error {
	err := r.Client.Create(ctx, buildDefaultAuth())
	if err != nil && !k8serr.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func buildDefaultAuth() client.Object {
	return &serviceApi.Auth{
		TypeMeta:   metav1.TypeMeta{Kind: serviceApi.AuthKind, APIVersion: serviceApi.GroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: serviceApi.AuthInstanceName},
		Spec:       serviceApi.AuthSpec{AdminGroups: []string{dashboard.GetAdminGroup()}, AllowedGroups: []string{"system:authenticated"}},
	}
}
