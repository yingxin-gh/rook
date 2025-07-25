/*
Copyright 2024 The Rook Authors. All rights reserved.

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

package csi

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	csiopv1 "github.com/ceph/ceph-csi-operator/api/v1"
	"github.com/pkg/errors"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8scsiv1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReconcileCSI) createOrUpdateDriverResources(cluster cephv1.CephCluster, clusterInfo *cephclient.ClusterInfo) error {
	if EnableRBD {
		logger.Info("Creating RBD driver resources")
		err := r.transferCSIDriverOwner(r.opManagerContext, RBDDriverName)
		if err != nil {
			return errors.Wrap(err, "failed to create update RBD driver for csi-operator driver CR ")
		}
		err = r.createOrUpdateRBDDriverResource(cluster, clusterInfo)
		if err != nil {
			return errors.Wrapf(err, "failed to create or update RBD driver resource in the namespace %q", r.opConfig.OperatorNamespace)
		}
	}
	if EnableCephFS {
		logger.Info("Creating CephFS driver resources")
		err := r.transferCSIDriverOwner(r.opManagerContext, CephFSDriverName)
		if err != nil {
			return errors.Wrap(err, "failed to create update CephFS driver for csi-operator driver CR ")
		}
		err = r.createOrUpdateCephFSDriverResource(cluster, clusterInfo)
		if err != nil {
			return errors.Wrapf(err, "failed to create or update cephFS driver resource in the namespace %q", r.opConfig.OperatorNamespace)
		}
	}
	if EnableNFS {
		logger.Info("Creating NFS driver resources")
		err := r.transferCSIDriverOwner(r.opManagerContext, NFSDriverName)
		if err != nil {
			return errors.Wrap(err, "failed to create update NFS driver for csi-operator driver CR ")
		}
		err = r.createOrUpdateNFSDriverResource(cluster, clusterInfo)
		if err != nil {
			return errors.Wrapf(err, "failed to create or update NFS driver resource in the namespace %q", r.opConfig.OperatorNamespace)
		}
	}

	return nil
}

func (r *ReconcileCSI) createOrUpdateRBDDriverResource(cluster cephv1.CephCluster, clusterInfo *cephclient.ClusterInfo) error {
	resourceName := fmt.Sprintf("%s.rbd.csi.ceph.com", r.opConfig.OperatorNamespace)
	spec, err := r.generateDriverSpec(cluster.Name)
	if err != nil {
		return err
	}

	rbdDriver := &csiopv1.Driver{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: r.opConfig.OperatorNamespace,
		},
		Spec: spec,
	}

	rbdDriver.Spec.ControllerPlugin.Resources = createDriverControllerPluginResources(rbdPluginResource)
	rbdDriver.Spec.Liveness = &csiopv1.LivenessSpec{
		MetricsPort: int(CSIParam.RBDLivenessMetricsPort),
	}
	rbdDriver.Spec.NodePlugin.Resources = createDriverNodePluginResouces(rbdProvisionerResource)
	rbdDriver.Spec.NodePlugin.UpdateStrategy = &v1.DaemonSetUpdateStrategy{
		Type: v1.RollingUpdateDaemonSetStrategyType,
	}

	if CSIParam.CSIDomainLabels != "" {
		domainLabels := strings.Split(CSIParam.CSIDomainLabels, ",")
		rbdDriver.Spec.NodePlugin.Topology = &csiopv1.TopologySpec{
			DomainLabels: domainLabels,
		}
	}

	if CSIParam.RBDPluginUpdateStrategy == "OnDelete" {
		rbdDriver.Spec.NodePlugin.UpdateStrategy = &v1.DaemonSetUpdateStrategy{
			Type: v1.OnDeleteDaemonSetStrategyType,
		}
	}

	err = r.createOrUpdateDriverResource(clusterInfo, rbdDriver)
	if err != nil {
		return errors.Wrapf(err, "failed to create or update RBD driver resource %q", rbdDriver.Name)
	}

	return nil
}

func (r *ReconcileCSI) createOrUpdateCephFSDriverResource(cluster cephv1.CephCluster, clusterInfo *cephclient.ClusterInfo) error {
	resourceName := fmt.Sprintf("%s.cephfs.csi.ceph.com", r.opConfig.OperatorNamespace)
	spec, err := r.generateDriverSpec(cluster.Name)
	if err != nil {
		return err
	}

	cephFsDriver := &csiopv1.Driver{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: r.opConfig.OperatorNamespace,
		},
		Spec: spec,
	}

	cephFsDriver.Spec.SnapshotPolicy = csiopv1.NoneSnapshotPolicy
	if CSIParam.VolumeGroupSnapshotCLIFlag != "" {
		cephFsDriver.Spec.SnapshotPolicy = csiopv1.VolumeGroupSnapshotPolicy
	}

	cephFsDriver.Spec.ControllerPlugin.Resources = createDriverControllerPluginResources(cephFSPluginResource)
	cephFsDriver.Spec.Liveness = &csiopv1.LivenessSpec{
		MetricsPort: int(CSIParam.CephFSLivenessMetricsPort),
	}

	cephFsDriver.Spec.NodePlugin.Resources = createDriverNodePluginResouces(cephFSProvisionerResource)
	cephFsDriver.Spec.NodePlugin.UpdateStrategy = &v1.DaemonSetUpdateStrategy{
		Type: v1.RollingUpdateDaemonSetStrategyType,
	}

	if CSIParam.CSIDomainLabels != "" {
		domainLabels := strings.Split(CSIParam.CSIDomainLabels, ",")
		cephFsDriver.Spec.NodePlugin.Topology = &csiopv1.TopologySpec{
			DomainLabels: domainLabels,
		}
	}

	if CSIParam.RBDPluginUpdateStrategy == "OnDelete" {
		cephFsDriver.Spec.NodePlugin.UpdateStrategy = &v1.DaemonSetUpdateStrategy{
			Type: v1.OnDeleteDaemonSetStrategyType,
		}
	}

	err = r.createOrUpdateDriverResource(clusterInfo, cephFsDriver)
	if err != nil {
		return errors.Wrapf(err, "failed to create or update cephFS driver resource %q", cephFsDriver.Name)
	}

	return nil
}

func (r *ReconcileCSI) createOrUpdateNFSDriverResource(cluster cephv1.CephCluster, clusterInfo *cephclient.ClusterInfo) error {
	resourceName := fmt.Sprintf("%s.nfs.csi.ceph.com", r.opConfig.OperatorNamespace)
	spec, err := r.generateDriverSpec(cluster.Name)
	if err != nil {
		return err
	}

	NFSDriver := &csiopv1.Driver{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: r.opConfig.OperatorNamespace,
		},
		Spec: spec,
	}

	NFSDriver.Spec.ControllerPlugin.Resources = createDriverControllerPluginResources(nfsPluginResource)

	NFSDriver.Spec.NodePlugin.Resources = createDriverNodePluginResouces(nfsProvisionerResource)
	NFSDriver.Spec.NodePlugin.UpdateStrategy = &v1.DaemonSetUpdateStrategy{
		Type: v1.RollingUpdateDaemonSetStrategyType,
	}

	if CSIParam.CSIDomainLabels != "" {
		domainLabels := strings.Split(CSIParam.CSIDomainLabels, ",")
		NFSDriver.Spec.NodePlugin.Topology = &csiopv1.TopologySpec{
			DomainLabels: domainLabels,
		}
	}

	if CSIParam.RBDPluginUpdateStrategy == "OnDelete" {
		NFSDriver.Spec.NodePlugin.UpdateStrategy = &v1.DaemonSetUpdateStrategy{
			Type: v1.OnDeleteDaemonSetStrategyType,
		}
	}

	err = r.createOrUpdateDriverResource(clusterInfo, NFSDriver)
	if err != nil {
		return errors.Wrapf(err, "failed to create or update NFS driver resource %q", NFSDriver.Name)
	}

	return nil
}

func (r ReconcileCSI) createOrUpdateDriverResource(clusterInfo *cephclient.ClusterInfo, driverResource *csiopv1.Driver) error {
	spec := driverResource.Spec

	err := r.client.Get(r.opManagerContext, types.NamespacedName{Name: driverResource.Name, Namespace: r.opConfig.OperatorNamespace}, driverResource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.client.Create(r.opManagerContext, driverResource)
			if err != nil {
				return errors.Wrapf(err, "failed to create CSI-operator driver CR %q", driverResource.Name)
			}

			logger.Infof("successfully created CSI driver cr %q", driverResource.Name)
			return nil
		}
		return errors.Wrapf(err, "failed to get CSI-operator  driver CR %q", opConfigCRName)
	}

	driverResource.Spec = spec
	err = r.client.Update(r.opManagerContext, driverResource)
	if err != nil {
		return errors.Wrapf(err, "failed to update CSI-operator driver CR %q", driverResource.Name)
	}

	logger.Infof("successfully updated CSI-operator driver resource %q", driverResource.Name)
	return nil
}

func (r *ReconcileCSI) generateDriverSpec(clusterName string) (csiopv1.DriverSpec, error) {
	cephfsClientType := csiopv1.KernelCephFsClient
	if CSIParam.ForceCephFSKernelClient == "false" {
		cephfsClientType = csiopv1.AutoDetectCephFsClient
	}
	imageSetCmName, err := r.createImageSetConfigmap()
	if err != nil {
		return csiopv1.DriverSpec{}, errors.Wrapf(err, "failed to create ceph-CSI operator config ImageSetConfigmap for CR %s", opConfigCRName)
	}

	return csiopv1.DriverSpec{
		Log: &csiopv1.LogSpec{
			Verbosity: int(CSIParam.LogLevel),
		},
		ImageSet: &corev1.LocalObjectReference{
			Name: imageSetCmName,
		},
		ClusterName:      &clusterName,
		EnableMetadata:   &CSIParam.CSIEnableMetadata,
		GenerateOMapInfo: &CSIParam.EnableOMAPGenerator,
		FsGroupPolicy:    k8scsiv1.FileFSGroupPolicy,
		NodePlugin: &csiopv1.NodePluginSpec{
			PodCommonSpec: csiopv1.PodCommonSpec{
				PrioritylClassName: &CSIParam.ProvisionerPriorityClassName,
				Affinity: &corev1.Affinity{
					NodeAffinity: getNodeAffinity(pluginNodeAffinityEnv, &corev1.NodeAffinity{}),
				},
				Tolerations: getToleration(pluginTolerationsEnv, []corev1.Toleration{}),
			},
			Resources:              csiopv1.NodePluginResourcesSpec{},
			KubeletDirPath:         CSIParam.KubeletDirPath,
			EnableSeLinuxHostMount: &CSIParam.EnablePluginSelinuxHostMount,
		},
		ControllerPlugin: &csiopv1.ControllerPluginSpec{
			PodCommonSpec: csiopv1.PodCommonSpec{
				PrioritylClassName: &CSIParam.PluginPriorityClassName,
				Affinity: &corev1.Affinity{
					NodeAffinity: getNodeAffinity(provisionerNodeAffinityEnv, &corev1.NodeAffinity{}),
				},
				Tolerations: getToleration(provisionerTolerationsEnv, []corev1.Toleration{}),
			},
			Replicas:  &CSIParam.ProvisionerReplicas,
			Resources: csiopv1.ControllerPluginResourcesSpec{},
		},
		DeployCsiAddons:  &CSIParam.EnableCSIAddonsSideCar,
		CephFsClientType: cephfsClientType,
	}, nil
}

func createDriverControllerPluginResources(key string) csiopv1.ControllerPluginResourcesSpec {
	controllerPluginResources := csiopv1.ControllerPluginResourcesSpec{}
	resource := getComputeResource(key)

	for _, r := range resource {
		if !reflect.DeepEqual(r.Resource, corev1.ResourceRequirements{}) {
			switch {
			case strings.Contains(r.Name, "provisioner"):
				controllerPluginResources.Provisioner = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			case strings.Contains(r.Name, "resizer"):
				controllerPluginResources.Resizer = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			case strings.Contains(r.Name, "snapshotter"):
				controllerPluginResources.Snapshotter = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			case strings.Contains(r.Name, "attacher"):
				controllerPluginResources.Attacher = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			case strings.Contains(r.Name, "plugin"):
				controllerPluginResources.Plugin = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			case strings.Contains(r.Name, "omap-generator"):
				controllerPluginResources.OMapGenerator = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			case strings.Contains(r.Name, "liveness"):
				controllerPluginResources.Liveness = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			case strings.Contains(r.Name, "addons"):
				controllerPluginResources.Addons = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			}
		}
	}
	return controllerPluginResources
}

func createDriverNodePluginResouces(key string) csiopv1.NodePluginResourcesSpec {
	nodePluginResources := csiopv1.NodePluginResourcesSpec{}
	resource := getComputeResource(key)

	for _, r := range resource {
		if !reflect.DeepEqual(r.Resource, corev1.ResourceRequirements{}) {
			if strings.Contains(r.Name, "registrar") {
				nodePluginResources.Registrar = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			} else if strings.Contains(r.Name, "plugin") {
				nodePluginResources.Plugin = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			} else if strings.Contains(r.Name, "liveness") {
				nodePluginResources.Liveness = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			} else if strings.Contains(r.Name, "addons") {
				nodePluginResources.Addons = &corev1.ResourceRequirements{
					Limits:   r.Resource.Limits,
					Requests: r.Resource.Requests,
				}
			}
		}
	}
	return nodePluginResources
}

// transferCSIDriverOwner update CSIDriver and returns the error if any
func (r *ReconcileCSI) transferCSIDriverOwner(ctx context.Context, name string) error {
	logger.Info("adding annotation to CSIDriver resource for csi-operator to own it")
	csiDriver, err := r.context.Clientset.StorageV1().CSIDrivers().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Debugf("%s CSIDriver not found; skipping ownership transfer.", name)
			return nil
		}
	}

	key := "csi.ceph.io/ownerref"
	ownerObjKey := client.ObjectKeyFromObject(csiDriver)
	ownerObjKey.Namespace = r.opConfig.OperatorNamespace
	val, err := json.Marshal(ownerObjKey)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal owner object key %q", ownerObjKey.Name)
	}

	annotations := csiDriver.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		csiDriver.SetAnnotations(annotations)
	}
	if oldValue, exist := annotations[key]; !exist || oldValue != string(val) {
		annotations[key] = string(val)
	} else {
		return nil
	}
	_, err = r.context.Clientset.StorageV1().CSIDrivers().Update(ctx, csiDriver, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to update CSIDriver %s", name)
	}

	return nil
}
