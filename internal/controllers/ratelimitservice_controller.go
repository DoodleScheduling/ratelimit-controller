/*


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

package controllers

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1beta1 "github.com/DoodleScheduling/ratelimit-controller/api/v1beta1"
	"github.com/DoodleScheduling/ratelimit-controller/internal/merge"
)

// +kubebuilder:rbac:groups=ratelimit.infra.doodle.com,resources=ratelimitservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ratelimit.infra.doodle.com,resources=ratelimitservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ratelimit.infra.doodle.com,resources=ratelimitrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;watch;list
// +kubebuilder:rbac:groups="",resources=services,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// RateLimitService reconciles a RateLimitService object
type RateLimitServiceReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

type RateLimitServiceReconcilerOptions struct {
	MaxConcurrentReconciles int
}

// SetupWithManager adding controllers
func (r *RateLimitServiceReconciler) SetupWithManager(mgr ctrl.Manager, opts RateLimitServiceReconcilerOptions) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1beta1.RateLimitService{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(
			&infrav1beta1.RateLimitRule{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForChangeBySelector),
		).
		Watches(
			&corev1.Service{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.RateLimitService{}, handler.OnlyControllerOwner()),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

func (r *RateLimitServiceReconciler) requestsForChangeBySelector(ctx context.Context, o client.Object) []reconcile.Request {
	var list infrav1beta1.RateLimitServiceList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace())); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	for _, service := range list.Items {
		labelSel, err := metav1.LabelSelectorAsSelector(service.Spec.RuleSelector)
		if err != nil {
			r.Log.Error(err, "can not select resourceSelector selectors")
			continue
		}

		if labelSel.Matches(labels.Set(o.GetLabels())) {
			r.Log.V(1).Info("referenced resource from a RateLimitService changed detected", "namespace", service.GetNamespace(), "service-name", service.GetName())
			reqs = append(reqs, reconcile.Request{NamespacedName: objectKey(&service)})
		}
	}

	return reqs
}

// Reconcile RateLimitServices
func (r *RateLimitServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.NamespacedName)
	logger.Info("reconciling RateLimitService")

	// Fetch the RateLimitService instance
	service := infrav1beta1.RateLimitService{}

	err := r.Client.Get(ctx, req.NamespacedName, &service)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if service.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	service, result, err := r.reconcile(ctx, service)
	service.Status.ObservedGeneration = service.GetGeneration()

	if err != nil {
		logger.Error(err, "reconcile error occurred")
		service = infrav1beta1.RateLimitServiceReady(service, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		r.Recorder.Event(&service, "Normal", "error", err.Error())
		result.Requeue = true
	}

	// Update status after reconciliation.
	if err := r.patchStatus(ctx, &service); err != nil {
		logger.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{Requeue: true}, err
	}

	return result, err
}

type yamlReplaces struct {
	Name string
}

type YamlRateLimit struct {
	RequestsPerUnit uint32 `yaml:"requests_per_unit"`
	Unit            string
	Unlimited       bool `yaml:"unlimited,omitempty"`
	Name            string
	Replaces        []yamlReplaces `yaml:"replaces,omitempty"`
}

type YamlDescriptor struct {
	Key            string
	Value          string            `yaml:"value,omitempty"`
	RateLimit      *YamlRateLimit    `yaml:"rate_limit,omitempty"`
	Descriptors    []*YamlDescriptor `yaml:"descriptors,omitempty"`
	ShadowMode     bool              `yaml:"shadow_mode,omitempty"`
	DetailedMetric bool              `yaml:"detailed_metric,omitempty"`
}

type YamlRoot struct {
	Domain      string
	Descriptors []*YamlDescriptor
}

func (r *RateLimitServiceReconciler) rulesToDescriptorSet(rules []infrav1beta1.RateLimitRule) map[string]*YamlRoot {
	domains := make(map[string]*YamlRoot)

	for _, rule := range rules {
		var lastDescriptor *YamlDescriptor

		if _, ok := domains[rule.Spec.Domain]; !ok {
			domains[rule.Spec.Domain] = &YamlRoot{
				Domain: rule.Spec.Domain,
			}
		}

		for _, descriptor := range rule.Spec.Descriptors {
			has := false

			var descriptors []*YamlDescriptor
			if lastDescriptor == nil {
				descriptors = domains[rule.Spec.Domain].Descriptors
			} else {
				descriptors = lastDescriptor.Descriptors
			}

			for _, cfgDescriptor := range descriptors {
				if cfgDescriptor.Key == descriptor.Key && cfgDescriptor.Value == descriptor.Value {
					has = true
					lastDescriptor = cfgDescriptor
					break
				}
			}

			if !has {
				newCfgDescriptor := &YamlDescriptor{
					Key:   descriptor.Key,
					Value: descriptor.Value,
				}

				if lastDescriptor == nil {
					domains[rule.Spec.Domain].Descriptors = append(domains[rule.Spec.Domain].Descriptors, newCfgDescriptor)
				} else {
					lastDescriptor.Descriptors = append(lastDescriptor.Descriptors, newCfgDescriptor)
				}

				lastDescriptor = newCfgDescriptor
			}
		}

		lastDescriptor.RateLimit = &YamlRateLimit{
			RequestsPerUnit: rule.Spec.RequestsPerUnit,
			Unit:            rule.Spec.Unit,
			Unlimited:       rule.Spec.Unlimited,
			Name:            rule.Name,
		}

		for _, v := range rule.Spec.Replaces {
			lastDescriptor.RateLimit.Replaces = append(lastDescriptor.RateLimit.Replaces, yamlReplaces{
				Name: v.Name,
			})
		}
	}

	return domains
}

func (r *RateLimitServiceReconciler) reconcile(ctx context.Context, service infrav1beta1.RateLimitService) (infrav1beta1.RateLimitService, ctrl.Result, error) {
	service.Status.SubResourceCatalog = []infrav1beta1.ResourceReference{}
	service, rules, err := r.extendserviceWithRateLimitRules(ctx, service)
	if err != nil {
		return service, ctrl.Result{}, err
	}

	var (
		gid          int64 = 10000
		uid          int64 = 10000
		runAsNonRoot bool  = true
		replicas     int32 = 1
	)

	controllerOwner := true

	cmTemplate := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("ratelimit-%s", service.Name),
			Namespace: service.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       service.Name,
					APIVersion: service.APIVersion,
					Kind:       service.Kind,
					UID:        service.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Data: make(map[string]string),
	}

	domains := r.rulesToDescriptorSet(rules)
	checksumSha := sha256.New()

	for domain, cfg := range domains {
		cfgYaml, err := yaml.Marshal(&cfg)
		if err != nil {
			return service, ctrl.Result{}, err
		}

		checksumSha.Write(cfgYaml)
		cmTemplate.Data[fmt.Sprintf("%s.yaml", domain)] = string(cfgYaml)
	}

	checksum := fmt.Sprintf("%x", checksumSha.Sum(nil))

	var cm corev1.ConfigMap
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: cmTemplate.Namespace,
		Name:      cmTemplate.Name,
	}, &cm)

	if err != nil && !apierrors.IsNotFound(err) {
		return service, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, cmTemplate); err != nil {
			return service, ctrl.Result{}, err
		}
	} else {
		if err := r.Client.Update(ctx, cmTemplate); err != nil {
			return service, ctrl.Result{}, err
		}
	}

	template := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("ratelimit-%s", service.Name),
			Namespace: service.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       service.Name,
					APIVersion: service.APIVersion,
					Kind:       service.Kind,
					UID:        service.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:    &uid,
						RunAsGroup:   &gid,
						RunAsNonRoot: &runAsNonRoot,
					},
				},
			},
		},
	}

	if service.Spec.DeploymentTemplate != nil {
		template.ObjectMeta.Labels = service.Spec.DeploymentTemplate.Labels
		template.ObjectMeta.Annotations = service.Spec.DeploymentTemplate.Annotations
		service.Spec.DeploymentTemplate.Spec.Template.DeepCopyInto(&template.Spec.Template)
		template.Spec.MinReadySeconds = service.Spec.DeploymentTemplate.Spec.MinReadySeconds
		template.Spec.Paused = service.Spec.DeploymentTemplate.Spec.Paused
		template.Spec.ProgressDeadlineSeconds = service.Spec.DeploymentTemplate.Spec.ProgressDeadlineSeconds
		template.Spec.Replicas = service.Spec.DeploymentTemplate.Spec.Replicas
		template.Spec.RevisionHistoryLimit = service.Spec.DeploymentTemplate.Spec.RevisionHistoryLimit
		template.Spec.Strategy = service.Spec.DeploymentTemplate.Spec.Strategy
	}

	if template.ObjectMeta.Labels == nil {
		template.ObjectMeta.Labels = make(map[string]string)
	}

	if template.Spec.Template.ObjectMeta.Labels == nil {
		template.Spec.Template.ObjectMeta.Labels = make(map[string]string)
	}

	template.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app.kubernetes.io/instance":   "ratelimit",
			"app.kubernetes.io/name":       "ratelimit",
			"ratelimit-controller/service": service.Name,
		},
	}

	if template.Spec.Replicas == nil {
		template.Spec.Replicas = &replicas
	}

	template.Spec.Template.ObjectMeta.Labels["app.kubernetes.io/instance"] = "ratelimit"
	template.Spec.Template.ObjectMeta.Labels["app.kubernetes.io/name"] = "ratelimit"
	template.Spec.Template.ObjectMeta.Labels["ratelimit-controller/service"] = service.Name
	template.ObjectMeta.Labels["app.kubernetes.io/instance"] = "ratelimit"
	template.ObjectMeta.Labels["app.kubernetes.io/name"] = "ratelimit"
	template.ObjectMeta.Labels["ratelimit-controller/service"] = service.Name

	if template.Annotations == nil {
		template.Annotations = make(map[string]string)
	}

	if template.Spec.Template.ObjectMeta.Annotations == nil {
		template.Spec.Template.ObjectMeta.Annotations = make(map[string]string)
	}

	template.Spec.Template.ObjectMeta.Annotations["ratelimit-controller/sha256-checksum"] = checksum

	containers := []corev1.Container{
		{
			Name:  "ratelimit",
			Image: "docker.io/envoyproxy/ratelimit:master",
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Port: intstr.IntOrString{StrVal: "http", Type: intstr.String},
						Path: "/healthcheck",
					},
				},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Port: intstr.IntOrString{StrVal: "http", Type: intstr.String},
						Path: "/healthcheck",
					},
				},
			},
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: 8080,
				},
				{
					ContainerPort: 8081,
				},
			},
			VolumeMounts: []corev1.VolumeMount{
				corev1.VolumeMount{
					MountPath: "/data/runtime/config",
					Name:      "config",
				},
			},
			Command: []string{
				"/bin/ratelimit",
			},
			Env: []corev1.EnvVar{
				corev1.EnvVar{
					Name:  "LOG_LEVEL",
					Value: "debug",
				},
				corev1.EnvVar{
					Name:  "RUNTIME_SUBDIRECTORY",
					Value: "runtime",
				},
				corev1.EnvVar{
					Name:  "RUNTIME_ROOT",
					Value: "/data",
				},
				corev1.EnvVar{
					Name:  "RUNTIME_WATCH_ROOT",
					Value: "false",
				},
				corev1.EnvVar{
					Name:  "RUNTIME_IGNOREDOTFILES",
					Value: "true",
				},
			},
		},
	}

	containers, err = merge.MergePatchContainers(containers, template.Spec.Template.Spec.Containers)
	if err != nil {
		return service, ctrl.Result{}, err
	}

	template.Spec.Template.Spec.Volumes = append(template.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: "config",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cmTemplate.Name,
				},
			},
		},
	})

	template.Spec.Template.Spec.Containers = containers
	r.Log.Info("create ratelimit deployment", "deployment-name", template.Name)

	svcTemplate := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("ratelimit-%s", service.Name),
			Namespace: service.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       service.Name,
					APIVersion: service.APIVersion,
					Kind:       service.Kind,
					UID:        service.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.IntOrString{StrVal: "http", Type: intstr.String},
				},
			},
			Selector: map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": service.Name,
			},
		},
	}

	var svc corev1.Service
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: svcTemplate.Namespace,
		Name:      svcTemplate.Name,
	}, &svc)

	if err != nil && !apierrors.IsNotFound(err) {
		return service, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, svcTemplate); err != nil {
			return service, ctrl.Result{}, err
		}
	} else {
		if err := r.Client.Update(ctx, svcTemplate); err != nil {
			return service, ctrl.Result{}, err
		}
	}

	var deployment appsv1.Deployment
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: template.Namespace,
		Name:      template.Name,
	}, &deployment)

	if err != nil && !apierrors.IsNotFound(err) {
		return service, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, template); err != nil {
			return service, ctrl.Result{}, err
		}

	} else {
		if err := r.Client.Update(ctx, template); err != nil {
			return service, ctrl.Result{}, err
		}
	}

	service = infrav1beta1.RateLimitServiceReady(service, metav1.ConditionTrue, "ReconciliationSuccessful", fmt.Sprintf("deployment/%s created", template.Name))
	return service, ctrl.Result{}, nil
}

func (r *RateLimitServiceReconciler) extendserviceWithRateLimitRules(ctx context.Context, service infrav1beta1.RateLimitService) (infrav1beta1.RateLimitService, []infrav1beta1.RateLimitRule, error) {
	var specifications infrav1beta1.RateLimitRuleList
	rateLimitRuleSelector, err := metav1.LabelSelectorAsSelector(service.Spec.RuleSelector)
	if err != nil {
		return service, nil, err
	}

	var namespaces corev1.NamespaceList
	if service.Spec.NamespaceSelector == nil {
		namespaces.Items = append(namespaces.Items, corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: service.Namespace,
			},
		})
	} else {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(service.Spec.NamespaceSelector)
		if err != nil {
			return service, nil, err
		}

		err = r.Client.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: namespaceSelector})
		if err != nil {
			return service, nil, err
		}
	}

	for _, namespace := range namespaces.Items {
		var namespacedRateLimitRule infrav1beta1.RateLimitRuleList
		err = r.Client.List(ctx, &namespacedRateLimitRule, client.InNamespace(namespace.Name), client.MatchingLabelsSelector{Selector: rateLimitRuleSelector})
		if err != nil {
			return service, nil, err
		}

		specifications.Items = append(specifications.Items, namespacedRateLimitRule.Items...)
	}

	slices.SortFunc(specifications.Items, func(a, b infrav1beta1.RateLimitRule) int {
		return cmp.Or(
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Namespace, b.Namespace),
		)
	})

	for _, client := range specifications.Items {
		service.Status.SubResourceCatalog = append(service.Status.SubResourceCatalog, infrav1beta1.ResourceReference{
			Kind:       client.Kind,
			Name:       client.Name,
			APIVersion: client.APIVersion,
		})
	}

	return service, specifications.Items, nil
}

func (r *RateLimitServiceReconciler) patchStatus(ctx context.Context, service *infrav1beta1.RateLimitService) error {
	key := client.ObjectKeyFromObject(service)
	latest := &infrav1beta1.RateLimitService{}
	if err := r.Client.Get(ctx, key, latest); err != nil {
		return err
	}

	return r.Client.Status().Patch(ctx, service, client.MergeFrom(latest))
}

// objectKey returns client.ObjectKey for the object.
func objectKey(object metav1.Object) client.ObjectKey {
	return client.ObjectKey{
		Namespace: object.GetNamespace(),
		Name:      object.GetName(),
	}
}
