/*
Copyright 2016 The Rook Authors. All rights reserved.

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

// Package file to manage a rook filesystem
package file

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/coreos/pkg/capnslog"
	"github.com/pkg/errors"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	rookclient "github.com/rook/rook/pkg/client/clientset/versioned/fake"
	"github.com/rook/rook/pkg/client/clientset/versioned/scheme"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/operator/ceph/file/mds"
	"github.com/rook/rook/pkg/operator/ceph/version"
	"github.com/rook/rook/pkg/operator/k8sutil"
	testopk8s "github.com/rook/rook/pkg/operator/k8sutil/test"
	"github.com/rook/rook/pkg/operator/test"
	"github.com/rook/rook/pkg/util/dependents"
	exectest "github.com/rook/rook/pkg/util/exec/test"
	"github.com/stretchr/testify/assert"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	fsGet = `{
		"mdsmap":{
		   "epoch":49,
		   "flags":50,
		   "ever_allowed_features":32,
		   "explicitly_allowed_features":32,
		   "created":"2020-03-17 13:17:43.743717",
		   "modified":"2020-03-17 15:22:51.020576",
		   "tableserver":0,
		   "root":0,
		   "session_timeout":60,
		   "session_autoclose":300,
		   "min_compat_client":"-1 (unspecified)",
		   "max_file_size":1099511627776,
		   "last_failure":0,
		   "last_failure_osd_epoch":0,
		   "compat":{
			  "compat":{

			  },
			  "ro_compat":{

			  },
			  "incompat":{
				 "feature_1":"base v0.20",
				 "feature_2":"client writeable ranges",
				 "feature_3":"default file layouts on dirs",
				 "feature_4":"dir inode in separate object",
				 "feature_5":"mds uses versioned encoding",
				 "feature_6":"dirfrag is stored in omap",
				 "feature_8":"no anchor table",
				 "feature_9":"file layout v2",
				 "feature_10":"snaprealm v2"
			  }
		   },
		   "max_mds":1,
		   "in":[
			  0
		   ],
		   "up":{
			  "mds_0":4463
		   },
		   "failed":[

		   ],
		   "damaged":[

		   ],
		   "stopped":[

		   ],
		   "info":{
			  "gid_4463":{
				 "gid":4463,
				 "name":"myfs-a",
				 "rank":0,
				 "incarnation":5,
				 "state":"up:active",
				 "state_seq":3,
				 "addr":"172.17.0.12:6801/175789278",
				 "addrs":{
					"addrvec":[
					   {
						  "type":"v2",
						  "addr":"172.17.0.12:6800",
						  "nonce":175789278
					   },
					   {
						  "type":"v1",
						  "addr":"172.17.0.12:6801",
						  "nonce":175789278
					   }
					]
				 },
				 "export_targets":[

				 ],
				 "features":4611087854031667199,
				 "flags":0
			  }
		   },
		   "data_pools":[
			  3
		   ],
		   "metadata_pool":2,
		   "enabled":true,
		   "fs_name":"myfs",
		   "balancer":"",
		   "standby_count_wanted":1
		},
		"id":1
	 }`
	mdsCephAuthGetOrCreateKey = `{"key":"AQCvzWBeIV9lFRAAninzm+8XFxbSfTiPwoX50g=="}`
	dummyVersionsRaw          = `
	{
		"mon": {
			"ceph version 19.2.1 (000000000000000000000000000000) squid (stable)": 3
		}
	}`
)

var (
	name      = "my-fs"
	namespace = "rook-ceph"
)

func TestCephFilesystemController(t *testing.T) {
	ctx := context.TODO()
	// Set DEBUG logging
	capnslog.SetGlobalLogLevel(capnslog.DEBUG)
	os.Setenv("ROOK_LOG_LEVEL", "DEBUG")

	currentAndDesiredCephVersion = func(ctx context.Context, rookImage string, namespace string, jobName string, ownerInfo *k8sutil.OwnerInfo, context *clusterd.Context, cephClusterSpec *cephv1.ClusterSpec, clusterInfo *client.ClusterInfo) (*version.CephVersion, *version.CephVersion, error) {
		return &version.Reef, &version.Reef, nil
	}

	// A Pool resource with metadata and spec.
	fs := &cephv1.CephFilesystem{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: []string{"cephfilesystem.ceph.rook.io"},
		},
		Spec: cephv1.FilesystemSpec{
			MetadataServer: cephv1.MetadataServerSpec{
				ActiveCount: 1,
			},
		},
		TypeMeta: controllerTypeMeta,
	}

	// Objects to track in the fake client.
	object := []runtime.Object{
		fs,
	}

	executor := &exectest.MockExecutor{
		MockExecuteCommandWithOutput: func(command string, args ...string) (string, error) {
			if args[0] == "status" {
				return `{"fsid":"c47cac40-9bee-4d52-823b-ccd803ba5bfe","health":{"checks":{},"status":"HEALTH_ERR"},"pgmap":{"num_pgs":100,"pgs_by_state":[{"state_name":"active+clean","count":100}]}}`, nil
			}
			if args[0] == "versions" {
				return dummyVersionsRaw, nil
			}
			return "", nil
		},
	}

	// Register operator types with the runtime scheme.
	s := scheme.Scheme
	s.AddKnownTypes(cephv1.SchemeGroupVersion, &cephv1.CephObjectStore{})
	s.AddKnownTypes(cephv1.SchemeGroupVersion, &cephv1.CephCluster{})

	// Create a fake client to mock API calls.
	cl := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(object...).Build()
	clientset := test.New(t, 3)
	c := &clusterd.Context{
		Executor:      executor,
		RookClientset: rookclient.NewSimpleClientset(),
		Clientset:     clientset,
		Client:        cl,
	}

	// Create a ReconcileCephFilesystem object with the scheme and fake client.
	r := &ReconcileCephFilesystem{
		client:           cl,
		recorder:         record.NewFakeRecorder(5),
		scheme:           s,
		context:          c,
		fsContexts:       make(map[string]*fsHealth),
		opManagerContext: context.TODO(),
	}

	// Mock request to simulate Reconcile() being called on an event for a
	// watched resource .
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}
	cephCluster := &cephv1.CephCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namespace,
			Namespace: namespace,
		},
		Spec: cephv1.ClusterSpec{
			Security: cephv1.ClusterSecuritySpec{
				CephX: cephv1.ClusterCephxConfig{
					Daemon: cephv1.CephxConfig{},
				},
			},
		},
		Status: cephv1.ClusterStatus{
			Phase: "",
			CephStatus: &cephv1.CephStatus{
				Health: "",
			},
		},
	}

	t.Run("error - no ceph cluster", func(t *testing.T) {
		res, err := r.Reconcile(ctx, req)
		assert.NoError(t, err)
		assert.True(t, res.Requeue)
	})

	t.Run("error - ceph cluster not ready", func(t *testing.T) {
		object = append(object, cephCluster)
		// Create a fake client to mock API calls.
		cl = fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(object...).Build()
		// Create a ReconcileCephFilesystem object with the scheme and fake client.
		r = &ReconcileCephFilesystem{
			client:           cl,
			recorder:         record.NewFakeRecorder(5),
			scheme:           s,
			context:          c,
			fsContexts:       make(map[string]*fsHealth),
			opManagerContext: context.TODO(),
		}
		res, err := r.Reconcile(ctx, req)
		assert.NoError(t, err)
		assert.True(t, res.Requeue)
		logger.Info("PHASE 2 DONE")
	})

	t.Run("success - ceph cluster ready and mds are running", func(t *testing.T) {
		// Mock clusterInfo
		secrets := map[string][]byte{
			"fsid":         []byte(name),
			"mon-secret":   []byte("monsecret"),
			"admin-secret": []byte("adminsecret"),
		}
		secret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rook-ceph-mon",
				Namespace: namespace,
			},
			Data: secrets,
			Type: k8sutil.RookType,
		}
		_, err := c.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		assert.NoError(t, err)

		// Add ready status to the CephCluster
		cephCluster.Status.Phase = k8sutil.ReadyStatus
		cephCluster.Status.CephStatus.Health = "HEALTH_OK"

		// Create a fake client to mock API calls.
		cl := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(fs, cephCluster).Build()

		executor = &exectest.MockExecutor{
			MockExecuteCommandWithOutput: func(command string, args ...string) (string, error) {
				if args[0] == "status" {
					return `{"fsid":"c47cac40-9bee-4d52-823b-ccd803ba5bfe","health":{"checks":{},"status":"HEALTH_OK"},"pgmap":{"num_pgs":100,"pgs_by_state":[{"state_name":"active+clean","count":100}]}}`, nil
				}
				if args[0] == "fs" && args[1] == "get" {
					return fsGet, nil
				}
				if args[0] == "auth" && args[1] == "get-or-create-key" {
					return mdsCephAuthGetOrCreateKey, nil
				}
				if args[0] == "versions" {
					return dummyVersionsRaw, nil
				}
				return "", nil
			},
		}
		c.Executor = executor
		c.Client = cl

		// Create a ReconcileCephFilesystem object with the scheme and fake client.
		r = &ReconcileCephFilesystem{
			client:           cl,
			recorder:         record.NewFakeRecorder(5),
			scheme:           s,
			context:          c,
			fsContexts:       make(map[string]*fsHealth),
			opManagerContext: context.TODO(),
		}

		res, err := r.Reconcile(ctx, req)
		assert.NoError(t, err)
		assert.False(t, res.Requeue)
		err = r.client.Get(context.TODO(), req.NamespacedName, fs)
		assert.NoError(t, err)
		assert.Equal(t, cephv1.ConditionType("Ready"), fs.Status.Phase, fs)
	})

	t.Run("block for dependents", func(t *testing.T) {
		clientset := test.New(t, 3)
		c := &clusterd.Context{
			Executor:      executor,
			RookClientset: rookclient.NewSimpleClientset(),
			Clientset:     clientset,
		}

		// Mock clusterInfo
		secrets := map[string][]byte{
			"fsid":         []byte(name),
			"mon-secret":   []byte("monsecret"),
			"admin-secret": []byte("adminsecret"),
		}
		secret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rook-ceph-mon",
				Namespace: namespace,
			},
			Data: secrets,
			Type: k8sutil.RookType,
		}
		_, err := c.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		assert.NoError(t, err)

		// Add ready status to the CephCluster
		cephCluster := cephCluster.DeepCopy()
		cephCluster.Status.Phase = k8sutil.ReadyStatus
		cephCluster.Status.CephStatus.Health = "HEALTH_OK"

		fs := fs.DeepCopy()
		fs.DeletionTimestamp = &metav1.Time{Time: time.Now()}

		// Create a fake client to mock API calls.
		cl = fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(fs, cephCluster).Build()

		executor = &exectest.MockExecutor{
			MockExecuteCommandWithOutput: func(command string, args ...string) (string, error) {
				if args[0] == "status" {
					return `{"fsid":"c47cac40-9bee-4d52-823b-ccd803ba5bfe","health":{"checks":{},"status":"HEALTH_OK"},"pgmap":{"num_pgs":100,"pgs_by_state":[{"state_name":"active+clean","count":100}]}}`, nil
				}
				if args[0] == "fs" && args[1] == "get" {
					return fsGet, nil
				}
				if args[0] == "auth" && args[1] == "get-or-create-key" {
					return mdsCephAuthGetOrCreateKey, nil
				}
				if args[0] == "versions" {
					return dummyVersionsRaw, nil
				}
				panic(fmt.Sprintf("unhandled MockExecuteCommandWithOutput command %q %v", command, args))
			},
		}
		c.Executor = executor

		// subvolume group to act as dependent
		cephFsSubvolGroup := &cephv1.CephFilesystemSubVolumeGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "group-a",
				Namespace: namespace,
			},
			Spec: cephv1.CephFilesystemSubVolumeGroupSpec{
				FilesystemName: name,
			},
		}
		_, err = c.RookClientset.CephV1().CephFilesystemSubVolumeGroups(namespace).Create(ctx, cephFsSubvolGroup, metav1.CreateOptions{})
		assert.NoError(t, err)

		// Create a ReconcileCephFilesystem object with the scheme and fake client.
		fakeRecorder := record.NewFakeRecorder(5)
		r = &ReconcileCephFilesystem{
			client:           cl,
			recorder:         fakeRecorder,
			scheme:           s,
			context:          c,
			fsContexts:       make(map[string]*fsHealth),
			opManagerContext: context.TODO(),
		}

		oldCephFSDeps := CephFilesystemDependents
		defer func() {
			CephFilesystemDependents = oldCephFSDeps
		}()

		t.Run("block on dependents", func(t *testing.T) {
			CephFilesystemDependents = func(
				clusterdCtx *clusterd.Context, clusterInfo *client.ClusterInfo, filesystem *cephv1.CephFilesystem,
			) (*dependents.DependentList, error) {
				deps := dependents.NewDependentList()
				deps.Add("TestDependent", "fake-dependent")
				return deps, nil
			}

			res, err := r.Reconcile(ctx, req)
			assert.NoError(t, err)
			assert.False(t, res.IsZero())
			assert.Len(t, fakeRecorder.Events, 1)
			event := <-fakeRecorder.Events
			assert.Contains(t, event, "TestDependent")
			assert.Contains(t, event, "fake-dependent")
		})
	})
}

func TestMdsKeyRotation(t *testing.T) {
	ctx := context.TODO()
	var deploymentsUpdated *[]*apps.Deployment
	mds.UpdateDeploymentAndWait, deploymentsUpdated = testopk8s.UpdateDeploymentAndWaitStub()
	currentAndDesiredCephVersion = func(ctx context.Context, rookImage string, namespace string, jobName string, ownerInfo *k8sutil.OwnerInfo, context *clusterd.Context, cephClusterSpec *cephv1.ClusterSpec, clusterInfo *client.ClusterInfo) (*version.CephVersion, *version.CephVersion, error) {
		rotateSupportedVer := version.CephVersion{Major: 20, Minor: 2, Extra: 0}
		return &rotateSupportedVer, &rotateSupportedVer, nil
	}
	fs := &cephv1.CephFilesystem{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: []string{"cephfilesystem.ceph.rook.io"},
		},
		Spec: cephv1.FilesystemSpec{
			MetadataServer: cephv1.MetadataServerSpec{
				ActiveCount: 1,
			},
		},
		TypeMeta: controllerTypeMeta,
	}

	cephCluster := &cephv1.CephCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namespace,
			Namespace: namespace,
		},
		Spec: cephv1.ClusterSpec{
			Security: cephv1.ClusterSecuritySpec{
				CephX: cephv1.ClusterCephxConfig{
					Daemon: cephv1.CephxConfig{},
				},
			},
		},
		Status: cephv1.ClusterStatus{
			Phase: "",
			CephStatus: &cephv1.CephStatus{
				Health: "HEALTH_OK",
			},
		},
	}

	object := []runtime.Object{
		fs, cephCluster,
	}

	s := scheme.Scheme
	s.AddKnownTypes(cephv1.SchemeGroupVersion, &cephv1.CephFilesystem{})
	s.AddKnownTypes(cephv1.SchemeGroupVersion, &cephv1.CephCluster{})

	// Create a fake client to mock API calls.
	cl := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(object...).Build()
	clientset := test.New(t, 3)
	mdsRotatedKey := `{"key":"AQCvzWBeIV9lFRAAninzm+8XFxbSfTiPwoX50g=="}`
	executor := &exectest.MockExecutor{
		MockExecuteCommandWithOutput: func(command string, args ...string) (string, error) {
			if args[0] == "status" {
				return `{"fsid":"c47cac40-9bee-4d52-823b-ccd803ba5bfe","health":{"checks":{},"status":"HEALTH_OK"},"pgmap":{"num_pgs":100,"pgs_by_state":[{"state_name":"active+clean","count":100}]}}`, nil
			}
			if args[0] == "auth" && args[1] == "get-or-create-key" {
				return `{"key":"AQCvzWBeIV9lFRAAninzm+8XFxbSfTiPwoX50g=="}`, nil
			}
			if args[0] == "auth" && args[1] == "rotate" {
				t.Logf("rotating key and returning: %s", mdsRotatedKey)
				return mdsRotatedKey, nil
			}
			if args[0] == "osd" && args[1] == "pool" && args[2] == "get" {
				return "", errors.New("test pool does not exit yet")
			}
			if args[0] == "versions" {
				return dummyVersionsRaw, nil
			}
			return "", nil
		},
	}
	c := &clusterd.Context{
		Executor:      executor,
		RookClientset: rookclient.NewSimpleClientset(),
		Clientset:     clientset,
		Client:        cl,
	}

	r := &ReconcileCephFilesystem{
		client:           cl,
		recorder:         record.NewFakeRecorder(6),
		scheme:           s,
		context:          c,
		fsContexts:       make(map[string]*fsHealth),
		opManagerContext: context.TODO(),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}

	t.Run("first reconcile", func(t *testing.T) {
		secrets := map[string][]byte{
			"fsid":         []byte(name),
			"mon-secret":   []byte("monsecret"),
			"admin-secret": []byte("adminsecret"),
		}
		secret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rook-ceph-mon",
				Namespace: namespace,
			},
			Data: secrets,
			Type: k8sutil.RookType,
		}
		_, err := c.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		assert.NoError(t, err)
		_, err = r.Reconcile(ctx, req)
		assert.NoError(t, err)
		fs := cephv1.CephFilesystem{}
		err = cl.Get(ctx, req.NamespacedName, &fs)
		assert.NoError(t, err)
		assert.Equal(t, uint32(1), fs.Status.Cephx.Daemon.KeyGeneration)
		assert.Equal(t, "20.2.0-0", fs.Status.Cephx.Daemon.KeyCephVersion)
	})

	t.Run("subsequent reconcile - retain cephx status", func(t *testing.T) {
		_, err := r.Reconcile(ctx, req)
		assert.NoError(t, err)
		fs := cephv1.CephFilesystem{}
		err = cl.Get(ctx, req.NamespacedName, &fs)
		assert.NoError(t, err)
		assert.Equal(t, uint32(1), fs.Status.Cephx.Daemon.KeyGeneration)
		assert.Equal(t, "20.2.0-0", fs.Status.Cephx.Daemon.KeyCephVersion)
		testopk8s.ClearDeploymentsUpdated(deploymentsUpdated)
	})

	t.Run("brownfield reconcile - retain unknown cephx status", func(t *testing.T) {
		fs := cephv1.CephFilesystem{}
		err := cl.Get(ctx, req.NamespacedName, &fs)
		assert.NoError(t, err)
		fs.Status.Cephx.Daemon = cephv1.CephxStatus{}
		err = cl.Update(ctx, &fs)
		assert.NoError(t, err)

		_, err = r.Reconcile(ctx, req)
		assert.NoError(t, err)

		err = cl.Get(ctx, req.NamespacedName, &fs)
		assert.NoError(t, err)
		assert.Equal(t, cephv1.CephxStatus{}, fs.Status.Cephx.Daemon)
	})
	t.Run("rotate key - brownfield unknown status becomes known", func(t *testing.T) {
		cluster := cephv1.CephCluster{}
		err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: namespace}, &cluster)
		assert.NoError(t, err)
		cluster.Spec.Security.CephX.Daemon = cephv1.CephxConfig{
			KeyRotationPolicy: "KeyGeneration",
			KeyGeneration:     2,
		}
		err = cl.Update(ctx, &cluster)
		assert.NoError(t, err)

		mdsRotatedKey = `[{"key":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=="}]`

		_, err = r.Reconcile(ctx, req)
		assert.NoError(t, err)

		oStore := cephv1.CephFilesystem{}
		err = cl.Get(ctx, req.NamespacedName, &oStore)
		assert.NoError(t, err)
		assert.Equal(t, uint32(2), oStore.Status.Cephx.Daemon.KeyGeneration)
		assert.Equal(t, "20.2.0-0", oStore.Status.Cephx.Daemon.KeyCephVersion)

		secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, "rook-ceph-mds-"+name+"-a-keyring", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Contains(t, secret.StringData["keyring"], "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB==")
	})

	t.Run("brownfield reconcile - no further rotation happens", func(t *testing.T) {
		// not expecting any rotation. So `ceph auth rotate` should not run and secret should not be updated
		mdsRotatedKey = `[{"key":"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=="}]`

		res, err := r.Reconcile(ctx, req)
		assert.NoError(t, err)
		assert.False(t, res.Requeue)

		fs := cephv1.CephFilesystem{}
		err = cl.Get(ctx, req.NamespacedName, &fs)
		assert.NoError(t, err)
		assert.Equal(t, uint32(2), fs.Status.Cephx.Daemon.KeyGeneration)
		assert.Equal(t, "20.2.0-0", fs.Status.Cephx.Daemon.KeyCephVersion)

		secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, "rook-ceph-mds-"+name+"-a-keyring", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NotContains(t, secret.StringData["keyring"], "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC==")
		testopk8s.ClearDeploymentsUpdated(deploymentsUpdated)
	})
	t.Run("rotate key - cephx status updated", func(t *testing.T) {
		cluster := cephv1.CephCluster{}
		err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: namespace}, &cluster)
		assert.NoError(t, err)
		cluster.Spec.Security.CephX.Daemon = cephv1.CephxConfig{
			KeyRotationPolicy: "KeyGeneration",
			KeyGeneration:     3,
		}
		err = cl.Update(ctx, &cluster)
		assert.NoError(t, err)

		mdsRotatedKey = `[{"key":"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=="}]`

		_, err = r.Reconcile(ctx, req)
		assert.NoError(t, err)

		fs := cephv1.CephFilesystem{}
		err = cl.Get(ctx, req.NamespacedName, &fs)
		assert.NoError(t, err)
		assert.Equal(t, uint32(3), fs.Status.Cephx.Daemon.KeyGeneration)
		assert.Equal(t, "20.2.0-0", fs.Status.Cephx.Daemon.KeyCephVersion)

		secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, "rook-ceph-mds-"+name+"-a-keyring", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Contains(t, secret.StringData["keyring"], "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC==")
	})
}
