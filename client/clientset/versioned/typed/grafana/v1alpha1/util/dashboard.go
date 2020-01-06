/*
Copyright The Searchlight Authors.

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

package util

import (
	"encoding/json"
	"fmt"

	api "go.searchlight.dev/grafana-operator/apis/grafana/v1alpha1"
	cs "go.searchlight.dev/grafana-operator/client/clientset/versioned/typed/grafana/v1alpha1"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kutil "kmodules.xyz/client-go"
)

func CreateOrPatchDashboard(c cs.GrafanaV1alpha1Interface, meta metav1.ObjectMeta, transform func(alert *api.Dashboard) *api.Dashboard) (*api.Dashboard, kutil.VerbType, error) {
	cur, err := c.Dashboards(meta.Namespace).Get(meta.Name, metav1.GetOptions{})
	if kerr.IsNotFound(err) {
		glog.V(3).Infof("Creating Dashboard %s/%s.", meta.Namespace, meta.Name)
		out, err := c.Dashboards(meta.Namespace).Create(transform(&api.Dashboard{
			TypeMeta: metav1.TypeMeta{
				Kind:       api.ResourceKindDashboard,
				APIVersion: api.SchemeGroupVersion.String(),
			},
			ObjectMeta: meta,
		}))
		return out, kutil.VerbCreated, err
	} else if err != nil {
		return nil, kutil.VerbUnchanged, err
	}
	return PatchDashboard(c, cur, transform)
}

func PatchDashboard(c cs.GrafanaV1alpha1Interface, cur *api.Dashboard, transform func(*api.Dashboard) *api.Dashboard) (*api.Dashboard, kutil.VerbType, error) {
	return PatchDashboardObject(c, cur, transform(cur.DeepCopy()))
}

func PatchDashboardObject(c cs.GrafanaV1alpha1Interface, cur, mod *api.Dashboard) (*api.Dashboard, kutil.VerbType, error) {
	curJson, err := json.Marshal(cur)
	if err != nil {
		return nil, kutil.VerbUnchanged, err
	}

	modJson, err := json.Marshal(mod)
	if err != nil {
		return nil, kutil.VerbUnchanged, err
	}

	patch, err := jsonpatch.CreateMergePatch(curJson, modJson)
	if err != nil {
		return nil, kutil.VerbUnchanged, err
	}
	if len(patch) == 0 || string(patch) == "{}" {
		return cur, kutil.VerbUnchanged, nil
	}
	glog.V(3).Infof("Patching Dashboard %s/%s with %s.", cur.Namespace, cur.Name, string(patch))
	out, err := c.Dashboards(cur.Namespace).Patch(cur.Name, types.MergePatchType, patch)
	return out, kutil.VerbPatched, err
}

func TryUpdateDashboard(c cs.GrafanaV1alpha1Interface, meta metav1.ObjectMeta, transform func(*api.Dashboard) *api.Dashboard) (result *api.Dashboard, err error) {
	attempt := 0
	err = wait.PollImmediate(kutil.RetryInterval, kutil.RetryTimeout, func() (bool, error) {
		attempt++
		cur, e2 := c.Dashboards(meta.Namespace).Get(meta.Name, metav1.GetOptions{})
		if kerr.IsNotFound(e2) {
			return false, e2
		} else if e2 == nil {
			result, e2 = c.Dashboards(cur.Namespace).Update(transform(cur.DeepCopy()))
			return e2 == nil, nil
		}
		glog.Errorf("Attempt %d failed to update Dashboard %s/%s due to %v.", attempt, cur.Namespace, cur.Name, e2)
		return false, nil
	})

	if err != nil {
		err = errors.Errorf("failed to update Dashboard %s/%s after %d attempts due to %v", meta.Namespace, meta.Name, attempt, err)
	}
	return
}

func UpdateDashboardStatus(
	c cs.GrafanaV1alpha1Interface,
	in *api.Dashboard,
	transform func(*api.DashboardStatus) *api.DashboardStatus,
) (result *api.Dashboard, err error) {
	apply := func(x *api.Dashboard, copy bool) *api.Dashboard {
		out := &api.Dashboard{
			TypeMeta:   x.TypeMeta,
			ObjectMeta: x.ObjectMeta,
			Spec:       x.Spec,
		}
		if copy {
			out.Status = *transform(in.Status.DeepCopy())
		} else {
			out.Status = *transform(&in.Status)
		}
		return out
	}

	attempt := 0
	cur := in.DeepCopy()
	err = wait.PollImmediate(kutil.RetryInterval, kutil.RetryTimeout, func() (bool, error) {
		attempt++
		var e2 error
		result, e2 = c.Dashboards(in.Namespace).UpdateStatus(apply(cur, false))
		if kerr.IsConflict(e2) {
			latest, e3 := c.Dashboards(in.Namespace).Get(in.Name, metav1.GetOptions{})
			switch {
			case e3 == nil:
				cur = latest
				return false, nil
			case kutil.IsRequestRetryable(e3):
				return false, nil
			default:
				return false, e3
			}
		} else if err != nil && !kutil.IsRequestRetryable(e2) {
			return false, e2
		}
		return e2 == nil, nil
	})

	if err != nil {
		err = fmt.Errorf("failed to update status of Dashboard %s/%s after %d attempts due to %v", in.Namespace, in.Name, attempt, err)
	}
	return
}