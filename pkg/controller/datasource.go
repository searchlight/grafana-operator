/*
Copyright AppsCode Inc. and Contributors

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
	"errors"

	api "go.searchlight.dev/grafana-operator/apis/grafana/v1alpha1"
	"go.searchlight.dev/grafana-operator/client/clientset/versioned/typed/grafana/v1alpha1/util"

	"github.com/golang/glog"
	"github.com/grafana-tools/sdk"
	"gomodules.xyz/pointer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	core_util "kmodules.xyz/client-go/core/v1"
	"kmodules.xyz/client-go/tools/queue"
)

const (
	DatasourceFinalizer = "datasource.grafana.searchlight.dev"
)

func (c *GrafanaController) initDatasourceWatcher() {
	c.datasourceInformer = c.extInformerFactory.Grafana().V1alpha1().Datasources().Informer()
	c.datasourceQueue = queue.New(api.ResourceKindDatasource, c.MaxNumRequeues, c.NumThreads, c.runDatasourceInjector)
	c.datasourceInformer.AddEventHandler(queue.NewReconcilableHandler(c.datasourceQueue.GetQueue()))
	c.datasourceLister = c.extInformerFactory.Grafana().V1alpha1().Datasources().Lister()
}

func (c *GrafanaController) runDatasourceInjector(key string) error {
	obj, exists, err := c.datasourceInformer.GetIndexer().GetByKey(key)
	if err != nil {
		glog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}
	if !exists {
		glog.Warningf("Datasource %s does not exist anymore\n", key)
	} else {
		ds := obj.(*api.Datasource).DeepCopy()
		glog.Infof("Sync/Add/Update for Datasource %s/%s\n", ds.Namespace, ds.Name)
		err := c.setGrafanaClient(ds.Namespace, ds.Spec.Grafana)
		if err != nil {
			return err
		}
		err = c.reconcileDatasource(ds)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *GrafanaController) reconcileDatasource(ds *api.Datasource) error {
	if ds.DeletionTimestamp != nil {
		if core_util.HasFinalizer(ds.ObjectMeta, DatasourceFinalizer) {
			err := c.runDatasourceFinalizer(ds)
			if err != nil {
				return err
			}
		}
		return nil
	}
	if !core_util.HasFinalizer(ds.ObjectMeta, DatasourceFinalizer) {
		// Add Finalizer
		_, _, err := util.PatchDatasource(context.TODO(), c.extClient.GrafanaV1alpha1(), ds, func(up *api.Datasource) *api.Datasource {
			up.ObjectMeta = core_util.AddFinalizer(ds.ObjectMeta, DatasourceFinalizer)
			return up
		}, metav1.PatchOptions{})
		if err != nil {
			return err
		}
		return nil
	}
	dataSrc := sdk.Datasource{
		OrgID:     uint(ds.Spec.OrgID),
		Name:      ds.Spec.Name,
		Type:      ds.Spec.Type,
		Access:    ds.Spec.Access,
		URL:       ds.Spec.URL,
		IsDefault: ds.Spec.IsDefault,
	}

	if ds.Status.DatasourceID != nil {
		dataSrc.ID = uint(pointer.Int64(ds.Status.DatasourceID))

		statusMsg, err := c.grafanaClient.UpdateDatasource(context.TODO(), dataSrc)
		if err != nil {
			return err
		}
		glog.Infof("Datasource is updated with message: %s\n", pointer.String(statusMsg.Message))
		return nil
	}
	statusMsg, err := c.grafanaClient.CreateDatasource(context.TODO(), dataSrc)
	if err != nil {
		return err
	}
	glog.Infof("Datasource is created with message: %s\n", pointer.String(statusMsg.Message))
	if statusMsg.ID != nil {
		ds.Status.DatasourceID = pointer.Int64P(int64(pointer.Uint(statusMsg.ID)))
	}
	_, err = c.extClient.GrafanaV1alpha1().Datasources(ds.Namespace).UpdateStatus(context.TODO(), ds, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func (c *GrafanaController) runDatasourceFinalizer(ds *api.Datasource) error {
	if ds.Status.DatasourceID == nil {
		return errors.New("datasource can't be deleted: reason: Datasource ID is missing")
	}
	dsID := uint(pointer.Int64(ds.Status.DatasourceID))
	statusMsg, err := c.grafanaClient.DeleteDatasource(context.TODO(), dsID)
	if err != nil {
		return err
	}
	glog.Infof("Datasource is deleted with message: %s\n", pointer.String(statusMsg.Message))

	// remove Finalizer
	_, _, err = util.PatchDatasource(context.TODO(), c.extClient.GrafanaV1alpha1(), ds, func(up *api.Datasource) *api.Datasource {
		up.ObjectMeta = core_util.RemoveFinalizer(ds.ObjectMeta, DatasourceFinalizer)
		return up
	}, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	return nil
}