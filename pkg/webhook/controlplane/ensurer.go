// Copyright (c) 2019 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controlplane

import (
	"bytes"
	"context"
	"fmt"
	"regexp"

	"github.com/gardener/gardener-extension-provider-gcp/pkg/gcp"
	"github.com/gardener/gardener-extension-provider-gcp/pkg/internal"
	extensionswebhook "github.com/gardener/gardener/extensions/pkg/webhook"
	"github.com/gardener/gardener/extensions/pkg/webhook/controlplane"
	"github.com/gardener/gardener/extensions/pkg/webhook/controlplane/genericmutator"

	"github.com/coreos/go-systemd/unit"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	versionutils "github.com/gardener/gardener/pkg/utils/version"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewEnsurer creates a new controlplane ensurer.
func NewEnsurer(logger logr.Logger) genericmutator.Ensurer {
	return &ensurer{
		logger: logger.WithName("gcp-controlplane-ensurer"),
	}
}

type ensurer struct {
	genericmutator.NoopEnsurer
	client client.Client
	logger logr.Logger
}

// InjectClient injects the given client into the ensurer.
func (e *ensurer) InjectClient(client client.Client) error {
	e.client = client
	return nil
}

// EnsureKubeAPIServerDeployment ensures that the kube-apiserver deployment conforms to the provider requirements.
func (e *ensurer) EnsureKubeAPIServerDeployment(ctx context.Context, ectx genericmutator.EnsurerContext, new, old *appsv1.Deployment) error {
	template := &new.Spec.Template
	ps := &template.Spec

	cluster, err := ectx.GetCluster(ctx)
	if err != nil {
		return err
	}

	if c := extensionswebhook.ContainerWithName(ps.Containers, "kube-apiserver"); c != nil {
		ensureKubeAPIServerCommandLineArgs(c)
		ensureEnvVars(c)
		ensureVolumeMounts(c, cluster.Shoot.Spec.Kubernetes.Version)
	}
	ensureVolumes(ps, cluster.Shoot.Spec.Kubernetes.Version)
	return e.ensureChecksumAnnotations(ctx, &new.Spec.Template, new.Namespace)
}

// EnsureKubeControllerManagerDeployment ensures that the kube-controller-manager deployment conforms to the provider requirements.
func (e *ensurer) EnsureKubeControllerManagerDeployment(ctx context.Context, ectx genericmutator.EnsurerContext, new, old *appsv1.Deployment) error {
	template := &new.Spec.Template
	ps := &template.Spec

	cluster, err := ectx.GetCluster(ctx)
	if err != nil {
		return err
	}

	if c := extensionswebhook.ContainerWithName(ps.Containers, "kube-controller-manager"); c != nil {
		ensureKubeControllerManagerCommandLineArgs(c)
		ensureEnvVars(c)
		ensureVolumeMounts(c, cluster.Shoot.Spec.Kubernetes.Version)
	}
	ensureKubeControllerManagerAnnotations(template)
	ensureVolumes(ps, cluster.Shoot.Spec.Kubernetes.Version)
	return e.ensureChecksumAnnotations(ctx, &new.Spec.Template, new.Namespace)
}

func ensureKubeAPIServerCommandLineArgs(c *corev1.Container) {
	c.Command = extensionswebhook.EnsureStringWithPrefix(c.Command, "--cloud-provider=", "gce")
	c.Command = extensionswebhook.EnsureStringWithPrefix(c.Command, "--cloud-config=",
		"/etc/kubernetes/cloudprovider/cloudprovider.conf")
	c.Command = extensionswebhook.EnsureStringWithPrefixContains(c.Command, "--enable-admission-plugins=",
		"PersistentVolumeLabel", ",")
	c.Command = extensionswebhook.EnsureNoStringWithPrefixContains(c.Command, "--disable-admission-plugins=",
		"PersistentVolumeLabel", ",")
}

func ensureKubeControllerManagerCommandLineArgs(c *corev1.Container) {
	c.Command = extensionswebhook.EnsureStringWithPrefix(c.Command, "--cloud-provider=", "external")
	c.Command = extensionswebhook.EnsureStringWithPrefix(c.Command, "--cloud-config=",
		"/etc/kubernetes/cloudprovider/cloudprovider.conf")
	c.Command = extensionswebhook.EnsureStringWithPrefix(c.Command, "--external-cloud-volume-plugin=", "gce")
}

func ensureKubeControllerManagerAnnotations(t *corev1.PodTemplateSpec) {
	t.Labels = extensionswebhook.EnsureAnnotationOrLabel(t.Labels, v1beta1constants.LabelNetworkPolicyToPublicNetworks, v1beta1constants.LabelNetworkPolicyAllowed)
	t.Labels = extensionswebhook.EnsureAnnotationOrLabel(t.Labels, v1beta1constants.LabelNetworkPolicyToPrivateNetworks, v1beta1constants.LabelNetworkPolicyAllowed)
	t.Labels = extensionswebhook.EnsureAnnotationOrLabel(t.Labels, v1beta1constants.LabelNetworkPolicyToBlockedCIDRs, v1beta1constants.LabelNetworkPolicyAllowed)
}

func ensureEnvVars(c *corev1.Container) {
	c.Env = extensionswebhook.EnsureEnvVarWithName(c.Env, credentialsEnvVar)
}

var (
	credentialsEnvVar = corev1.EnvVar{
		Name:  "GOOGLE_APPLICATION_CREDENTIALS",
		Value: fmt.Sprintf("/srv/cloudprovider/%s", gcp.ServiceAccountJSONField),
	}
	etcSSLName = "etc-ssl"

	cloudProviderConfigVolumeMount = corev1.VolumeMount{
		Name:      internal.CloudProviderConfigName,
		MountPath: "/etc/kubernetes/cloudprovider",
	}
	cloudProviderSecretVolumeMount = corev1.VolumeMount{
		Name:      v1beta1constants.SecretNameCloudProvider,
		MountPath: "/srv/cloudprovider",
	}
	etcSSLVolumeMount = corev1.VolumeMount{
		Name:      etcSSLName,
		MountPath: "/etc/ssl",
		ReadOnly:  true,
	}

	cloudProviderConfigVolume = corev1.Volume{
		Name: internal.CloudProviderConfigName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: internal.CloudProviderConfigName},
			},
		},
	}
	cloudProviderSecretVolume = corev1.Volume{
		Name: v1beta1constants.SecretNameCloudProvider,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: v1beta1constants.SecretNameCloudProvider,
			},
		},
	}
	etcSSLVolume = corev1.Volume{
		Name: etcSSLName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/etc/ssl",
			},
		},
	}
)

func ensureVolumeMounts(c *corev1.Container, version string) {
	c.VolumeMounts = extensionswebhook.EnsureVolumeMountWithName(c.VolumeMounts, cloudProviderConfigVolumeMount)
	c.VolumeMounts = extensionswebhook.EnsureVolumeMountWithName(c.VolumeMounts, cloudProviderSecretVolumeMount)

	if mustMountEtcSSLFolder(version) {
		c.VolumeMounts = extensionswebhook.EnsureVolumeMountWithName(c.VolumeMounts, etcSSLVolumeMount)
	}
}

func ensureVolumes(ps *corev1.PodSpec, version string) {
	ps.Volumes = extensionswebhook.EnsureVolumeWithName(ps.Volumes, cloudProviderConfigVolume)
	ps.Volumes = extensionswebhook.EnsureVolumeWithName(ps.Volumes, cloudProviderSecretVolume)

	if mustMountEtcSSLFolder(version) {
		ps.Volumes = extensionswebhook.EnsureVolumeWithName(ps.Volumes, etcSSLVolume)
	}
}

// Beginning with 1.17 Gardener no longer uses the hyperkube image for the Kubernetes control plane components.
// The hyperkube image contained all the well-known root CAs, but the dedicated images don't. This is why we
// mount the /etc/ssl folder from the host here.
// TODO: This can be remove again once we have migrated to CSI.
func mustMountEtcSSLFolder(version string) bool {
	k8sVersionAtLeast117, err := versionutils.CompareVersions(version, ">=", "1.17")
	if err != nil {
		return false
	}
	return k8sVersionAtLeast117
}

func (e *ensurer) ensureChecksumAnnotations(ctx context.Context, template *corev1.PodTemplateSpec, namespace string) error {
	if err := controlplane.EnsureSecretChecksumAnnotation(ctx, template, e.client, namespace, v1beta1constants.SecretNameCloudProvider); err != nil {
		return err
	}
	return controlplane.EnsureConfigMapChecksumAnnotation(ctx, template, e.client, namespace, internal.CloudProviderConfigName)
}

// EnsureKubeletServiceUnitOptions ensures that the kubelet.service unit options conform to the provider requirements.
func (e *ensurer) EnsureKubeletServiceUnitOptions(ctx context.Context, ectx genericmutator.EnsurerContext, new, old []*unit.UnitOption) ([]*unit.UnitOption, error) {
	if opt := extensionswebhook.UnitOptionWithSectionAndName(new, "Service", "ExecStart"); opt != nil {
		command := extensionswebhook.DeserializeCommandLine(opt.Value)
		command = ensureKubeletCommandLineArgs(command)
		opt.Value = extensionswebhook.SerializeCommandLine(command, 1, " \\\n    ")
	}
	new = extensionswebhook.EnsureUnitOption(new, &unit.UnitOption{
		Section: "Service",
		Name:    "ExecStartPre",
		Value:   `/bin/sh -c 'hostnamectl set-hostname $(wget -q -O- --header "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/hostname | cut -d '.' -f 1)'`,
	})
	return new, nil
}

func ensureKubeletCommandLineArgs(command []string) []string {
	command = extensionswebhook.EnsureStringWithPrefix(command, "--cloud-provider=", "gce")
	return command
}

// EnsureKubeletConfiguration ensures that the kubelet configuration conforms to the provider requirements.
func (e *ensurer) EnsureKubeletConfiguration(ctx context.Context, ectx genericmutator.EnsurerContext, new, old *kubeletconfigv1beta1.KubeletConfiguration) error {
	// Make sure CSI-related feature gates are not enabled
	// TODO Leaving these enabled shouldn't do any harm, perhaps remove this code when properly tested?
	delete(new.FeatureGates, "VolumeSnapshotDataSource")
	delete(new.FeatureGates, "CSINodeInfo")
	delete(new.FeatureGates, "CSIDriverRegistry")
	return nil
}

var regexFindProperty = regexp.MustCompile("net.ipv4.ip_forward[[:space:]]*=[[:space:]]*([[:alnum:]]+)")

// EnsureKubernetesGeneralConfiguration ensures that the kubernetes general configuration conforms to the provider requirements.
func (e *ensurer) EnsureKubernetesGeneralConfiguration(ctx context.Context, ectx genericmutator.EnsurerContext, new, old *string) error {
	// If the needed property exists, ensure the correct value
	if regexFindProperty.MatchString(*new) {
		res := regexFindProperty.ReplaceAll([]byte(*new), []byte("net.ipv4.ip_forward = 1"))
		*new = string(res)
		return nil
	}

	// If the property do not exist, append it in the end of the string
	buf := bytes.Buffer{}
	buf.WriteString(*new)
	buf.WriteString("\n")
	buf.WriteString("# GCE specific settings\n")
	buf.WriteString("net.ipv4.ip_forward = 1")

	*new = buf.String()
	return nil
}
