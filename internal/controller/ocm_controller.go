/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/openmcp-operator/lib/clusteraccess"

	apiv1alpha1 "github.com/open-component-model/service-provider-ocm/api/v1alpha1"
	spruntime "github.com/open-component-model/service-provider-ocm/pkg/runtime"
)

const (
	// HelmReleaseName is the name of the helm releases. Wow, such comment, much useful.
	HelmReleaseName = "helm-release"
	// OCIRepositoryName is the name of the oci repository. Wow, such comment, much useful.
	OCIRepositoryName = "oci-repository"
	requestSuffixMCP  = "--mcp"
)

var clusterAccessName = apiv1alpha1.GroupVersion.Group

// OCMReconciler reconciles a OCM object
type OCMReconciler struct {
	// OnboardingCluster is the cluster where this controller watches OCM resources and reacts to their changes.
	OnboardingCluster *clusters.Cluster
	// PlatformCluster is the cluster where this controller is deployed and configured.
	PlatformCluster *clusters.Cluster
	// PodNamespace is the namespace where this controller is deployed in.
	PodNamespace string
}

// CreateOrUpdate is called on every add or update event
func (r *OCMReconciler) CreateOrUpdate(ctx context.Context, svcobj *apiv1alpha1.OCM, providerConfig *apiv1alpha1.ProviderConfig, clusters spruntime.ClusterContext) (ctrl.Result, error) {
	l := logf.FromContext(ctx)
	l.Info("Reconciling OCM resource", "name", svcobj.Name)
	spruntime.StatusProgressing(svcobj, "Reconciling", "Reconcile in progress")
	tenantNamespace, err := libutils.StableMCPNamespace(svcobj.Name, svcobj.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to determine stable namespace for OCM instance: %w", err)
	}

	l.Info("checking tenantNamespace", "namespace", tenantNamespace)

	if err := r.replicateImagePullSecret(ctx, providerConfig, tenantNamespace); err != nil {
		spruntime.StatusFailed(svcobj, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to replicate image pull secret: %w", err)
	}

	if err := r.createOrUpdateOCIRepository(ctx, svcobj, clusters, tenantNamespace, providerConfig); err != nil {
		spruntime.StatusFailed(svcobj, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to reconcile OCI Repository: %w", err)
	}
	if err := r.createOrUpdateHelmRelease(ctx, tenantNamespace, svcobj.Name, providerConfig); err != nil {
		spruntime.StatusFailed(svcobj, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to reconcile HelmRelease: %w", err)
	}

	l.Info("Done reconciling OCM resource", "name", svcobj.Name)

	spruntime.StatusReady(svcobj)
	return ctrl.Result{}, nil
}

// Delete is called on every delete event
func (r *OCMReconciler) Delete(ctx context.Context, obj *apiv1alpha1.OCM, providerConfig *apiv1alpha1.ProviderConfig, _ spruntime.ClusterContext) (ctrl.Result, error) {
	spruntime.StatusTerminating(obj)

	tenantNamespace, err := libutils.StableMCPNamespace(obj.Name, obj.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to determine stable namespace for OCM instance: %w", err)
	}

	var objects []client.Object
	ociRepository := createOciRepository(obj.Spec.URL, obj.Spec.Version, tenantNamespace, providerConfig)
	objects = append(objects, ociRepository)
	helmRelease, err := r.createHelmRelease(ctx, tenantNamespace, obj.Name, providerConfig)
	if err != nil {
		spruntime.StatusFailed(obj, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to create helm release: %w", err)
	}
	objects = append(objects, helmRelease)

	objectsStillExist := false
	for _, managedObj := range objects {
		if err := r.PlatformCluster.Client().Delete(ctx, managedObj); client.IgnoreNotFound(err) != nil {
			spruntime.StatusFailed(obj, err.Error())
			return ctrl.Result{}, fmt.Errorf("delete object failed: %w", err)
		}
		// we ignore any other error because we assume that if deleting worked, getting should not fail with anything other than
		// not found.
		if err := r.PlatformCluster.Client().Get(ctx, client.ObjectKeyFromObject(managedObj), managedObj); err != nil {
			objectsStillExist = true
		}
	}

	if objectsStillExist {
		return ctrl.Result{
			RequeueAfter: time.Second * 10,
		}, nil
	}

	spruntime.StatusReady(obj)
	return ctrl.Result{}, nil
}

func (r *OCMReconciler) getMcpFluxConfig(ctx context.Context, namespace, objectName string) (*meta.SecretKeyReference, error) {
	mcpAccessRequest := &clustersv1alpha1.AccessRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusteraccess.StableRequestNameFromLocalName(clusterAccessName, objectName) + requestSuffixMCP,
			Namespace: namespace,
		},
	}

	if err := r.PlatformCluster.Client().Get(ctx, client.ObjectKeyFromObject(mcpAccessRequest), mcpAccessRequest); err != nil {
		return nil, fmt.Errorf("failed to get MCP AccessRequest: %w", err)
	}

	return &meta.SecretKeyReference{
		Name: mcpAccessRequest.Status.SecretRef.Name,
		Key:  "kubeconfig",
	}, nil
}

func (r *OCMReconciler) replicateImagePullSecret(ctx context.Context, providerConfig *apiv1alpha1.ProviderConfig, targetNamespace string) error {
	if providerConfig == nil || providerConfig.Spec.ImagePullSecret == nil {
		return nil
	}

	ref := providerConfig.Spec.ImagePullSecret
	platformClient := r.PlatformCluster.Client()

	sourceSecret := &corev1.Secret{}
	sourceKey := client.ObjectKey{Name: ref.Name, Namespace: r.PodNamespace}
	if err := platformClient.Get(ctx, sourceKey, sourceSecret); err != nil {
		return fmt.Errorf("failed to get image pull secret %q from namespace %q: %w", ref.Name, r.PodNamespace, err)
	}

	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ref.Name,
			Namespace: targetNamespace,
		},
	}
	if _, err := ctrl.CreateOrUpdate(ctx, platformClient, targetSecret, func() error {
		targetSecret.Data = sourceSecret.Data
		targetSecret.Type = sourceSecret.Type
		return nil
	}); err != nil {
		return fmt.Errorf("failed to replicate image pull secret %q to namespace %q: %w", ref.Name, targetNamespace, err)
	}

	return nil
}

func (r *OCMReconciler) createOrUpdateOCIRepository(ctx context.Context, svcobj *apiv1alpha1.OCM, _ spruntime.ClusterContext, namespace string, providerConfig *apiv1alpha1.ProviderConfig) error {
	ociRepository := createOciRepository(svcobj.Spec.URL, svcobj.Spec.Version, namespace, providerConfig)
	managedObj := &sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ociRepository.Name,
			Namespace: namespace,
		},
	}
	l := logf.FromContext(ctx)
	l.Info("creating OCI Repository", "object", ociRepository)
	if _, err := ctrl.CreateOrUpdate(ctx, r.PlatformCluster.Client(), managedObj, func() error {
		managedObj.Spec = ociRepository.Spec
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (r *OCMReconciler) createOrUpdateHelmRelease(ctx context.Context, namespace, objectName string, providerConfig *apiv1alpha1.ProviderConfig) error {
	helmRelease, err := r.createHelmRelease(ctx, namespace, objectName, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create helm release: %w", err)
	}
	managedObj := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmRelease.Name,
			Namespace: namespace,
		},
	}
	l := logf.FromContext(ctx)
	l.Info("creating Helm Release", "object", managedObj)
	if _, err := ctrl.CreateOrUpdate(ctx, r.PlatformCluster.Client(), managedObj, func() error {
		managedObj.Spec = helmRelease.Spec
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func ensureOCIScheme(url string) string {
	if !strings.HasPrefix(url, "oci://") {
		return "oci://" + url
	}
	return url
}

func createOciRepository(url, version, namespace string, providerConfig *apiv1alpha1.ProviderConfig) *sourcev1.OCIRepository {
	interval := metav1.Duration{Duration: time.Minute}
	if providerConfig != nil && providerConfig.Spec.HelmConfig != nil && providerConfig.Spec.HelmConfig.Interval != nil {
		interval = *providerConfig.Spec.HelmConfig.Interval
	}

	var secretRef *meta.LocalObjectReference
	if providerConfig != nil && providerConfig.Spec.ImagePullSecret != nil {
		secretRef = &meta.LocalObjectReference{
			Name: providerConfig.Spec.ImagePullSecret.Name,
		}
	}

	return &sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      OCIRepositoryName,
			Namespace: namespace,
		},
		Spec: sourcev1.OCIRepositorySpec{
			Interval:  interval,
			URL:       ensureOCIScheme(url),
			SecretRef: secretRef,
			Reference: &sourcev1.OCIRepositoryRef{
				Tag: version,
			},
		},
	}
}

func (r *OCMReconciler) createHelmRelease(ctx context.Context, namespace, objectName string, providerConfig *apiv1alpha1.ProviderConfig) (*helmv2.HelmRelease, error) {
	fluxConfigRef, err := r.getMcpFluxConfig(ctx, namespace, objectName)
	if err != nil {
		return nil, fmt.Errorf("failed to get FluxConfig: %w", err)
	}

	targetNamespace := metav1.NamespaceDefault
	interval := metav1.Duration{Duration: time.Minute}

	var helmConfig *apiv1alpha1.HelmConfig
	if providerConfig != nil {
		helmConfig = providerConfig.Spec.HelmConfig
	}

	if helmConfig != nil {
		if helmConfig.TargetNamespace != nil {
			targetNamespace = *helmConfig.TargetNamespace
		}
		if helmConfig.Interval != nil {
			interval = *helmConfig.Interval
		}
	}

	var helmValues *apiextensionsv1.JSON
	if helmConfig != nil && helmConfig.Values != nil {
		helmValues = helmConfig.Values
	}

	install, upgrade := buildHelmActions(providerConfig)

	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      HelmReleaseName,
			Namespace: namespace,
		},
		Spec: helmv2.HelmReleaseSpec{
			Interval:         interval,
			TargetNamespace:  targetNamespace,
			StorageNamespace: targetNamespace,
			Install:          install,
			Upgrade:          upgrade,
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "OCIRepository",
				Name:      OCIRepositoryName,
				Namespace: namespace,
			},
			Values: helmValues,
			KubeConfig: &meta.KubeConfigReference{
				SecretRef: fluxConfigRef,
			},
		},
	}, nil
}

func buildHelmActions(providerConfig *apiv1alpha1.ProviderConfig) (*helmv2.Install, *helmv2.Upgrade) {
	var helmConfig *apiv1alpha1.HelmConfig
	if providerConfig != nil {
		helmConfig = providerConfig.Spec.HelmConfig
	}

	return buildInstall(helmConfig), buildUpgrade(helmConfig)
}

func buildInstall(cfg *apiv1alpha1.HelmConfig) *helmv2.Install {
	crds := helmv2.Create
	retries := 3
	createNamespace := true

	if cfg != nil && cfg.Install != nil {
		ic := cfg.Install
		if ic.CRDs != nil {
			crds = helmv2.CRDsPolicy(*ic.CRDs)
		}
		if ic.Retries != nil {
			retries = *ic.Retries
		}
		if ic.CreateNamespace != nil {
			createNamespace = *ic.CreateNamespace
		}
	}

	return &helmv2.Install{
		CRDs:            crds,
		CreateNamespace: createNamespace,
		Remediation: &helmv2.InstallRemediation{
			Retries: retries,
		},
	}
}

func buildUpgrade(cfg *apiv1alpha1.HelmConfig) *helmv2.Upgrade {
	crds := helmv2.CreateReplace
	retries := 3
	cleanupOnFail := true
	force := false
	remediationStrategy := helmv2.RollbackRemediationStrategy

	if cfg != nil && cfg.Upgrade != nil {
		uc := cfg.Upgrade
		if uc.CRDs != nil {
			crds = helmv2.CRDsPolicy(*uc.CRDs)
		}
		if uc.Retries != nil {
			retries = *uc.Retries
		}
		if uc.CleanupOnFail != nil {
			cleanupOnFail = *uc.CleanupOnFail
		}
		if uc.Force != nil {
			force = *uc.Force
		}
		if uc.RemediationStrategy != nil {
			remediationStrategy = helmv2.RemediationStrategy(*uc.RemediationStrategy)
		}
	}

	return &helmv2.Upgrade{
		CRDs:          crds,
		CleanupOnFail: cleanupOnFail,
		Force:         force,
		Remediation: &helmv2.UpgradeRemediation{
			Retries:  retries,
			Strategy: &remediationStrategy,
		},
	}
}
