package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/DoodleScheduling/ratelimit-controller/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func needExactStatus(reconciledInstance *v1beta1.RateLimitService, expectedStatus *v1beta1.RateLimitServiceStatus) error {
	var expectedConditions []string
	var currentConditions []string

	for _, expectedCondition := range expectedStatus.Conditions {
		expectedConditions = append(expectedConditions, expectedCondition.Type)
		var hasCondition bool
		for _, condition := range reconciledInstance.Status.Conditions {
			if expectedCondition.Type == condition.Type {
				hasCondition = true

				if expectedCondition.Status != condition.Status {
					return fmt.Errorf("condition %s does not match expected status %s, current status=%s; current conditions=%#v", expectedCondition.Type, expectedCondition.Status, condition.Status, reconciledInstance.Status.Conditions)
				}
				if expectedCondition.Reason != condition.Reason {
					return fmt.Errorf("condition %s does not match expected reason %s, current reason=%s; current conditions=%#v", expectedCondition.Type, expectedCondition.Reason, condition.Reason, reconciledInstance.Status.Conditions)
				}
				if expectedCondition.Message != condition.Message {
					return fmt.Errorf("condition %s does not match expected message %s, current status=%s; current conditions=%#v", expectedCondition.Type, expectedCondition.Message, condition.Message, reconciledInstance.Status.Conditions)
				}
			}
		}

		if !hasCondition {
			return fmt.Errorf("missing condition %s", expectedCondition.Type)
		}
	}

	for _, condition := range reconciledInstance.Status.Conditions {
		currentConditions = append(currentConditions, condition.Type)
	}

	if len(expectedConditions) != len(currentConditions) {
		return fmt.Errorf("expected conditions %#v do not match, current conditions=%#v", expectedConditions, currentConditions)
	}

	return nil
}

var _ = Describe("RateLimitService controller", func() {
	const (
		timeout  = time.Second * 2
		interval = time.Millisecond * 100
	)

	var eventuallyMatchExactConditions = func(ctx context.Context, instanceLookupKey types.NamespacedName, reconciledInstance *v1beta1.RateLimitService, expectedStatus *v1beta1.RateLimitServiceStatus) {
		Eventually(func() error {
			err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			if err != nil {
				return err
			}

			return needExactStatus(reconciledInstance, expectedStatus)
		}, timeout, interval).Should(BeNil())
	}

	When("reconciling a suspended RateLimitService", func() {
		serviceName := fmt.Sprintf("service-%s", randStringRunes(5))

		It("should not update the status", func() {
			By("creating a new RateLimitService")
			ctx := context.Background()

			gi := &v1beta1.RateLimitService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitServiceSpec{
					Suspend: true,
				},
			}
			Expect(k8sClient.Create(ctx, gi)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: serviceName, Namespace: "default"}
			reconciledInstance := &v1beta1.RateLimitService{}

			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, &v1beta1.RateLimitServiceStatus{})
		})
	})

	When("it reconciles a service without rules", func() {
		serviceName := fmt.Sprintf("service-%s", randStringRunes(5))
		var service *v1beta1.RateLimitService

		It("creates a new service", func() {
			ctx := context.Background()

			service = &v1beta1.RateLimitService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitServiceSpec{},
			}
			Expect(k8sClient.Create(ctx, service)).Should(Succeed())
		})

		It("should create a service", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("ratelimit-%s", serviceName), Namespace: "default"}
			reconciledInstance := &corev1.Service{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Selector).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(serviceName))
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("ratelimit-%s", serviceName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("ratelimit"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("docker.io/envoyproxy/ratelimit:master"))
			Expect(len(reconciledInstance.Spec.Template.Spec.Containers[0].Env)).To(Equal(4))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(serviceName))
		})

		It("should update the service status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: serviceName, Namespace: "default"}
			reconciledInstance := &v1beta1.RateLimitService{}

			expectedStatus := &v1beta1.RateLimitServiceStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/ratelimit-%s created", serviceName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(len(reconciledInstance.Status.SubResourceCatalog)).Should(Equal(0))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, service)).Should(Succeed())
		})
	})

	When("it reconciles a service with rules", func() {
		serviceName := fmt.Sprintf("service-%s", randStringRunes(5))
		spec1Name := fmt.Sprintf("spec-b-%s", randStringRunes(5))
		spec2Name := fmt.Sprintf("spec-a-%s", randStringRunes(5))
		var service *v1beta1.RateLimitService
		var spec1 *v1beta1.RateLimitRule
		var spec2 *v1beta1.RateLimitRule

		It("creates a new service", func() {
			ctx := context.Background()

			service = &v1beta1.RateLimitService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitServiceSpec{
					RuleSelector: &metav1.LabelSelector{},
				},
			}
			Expect(k8sClient.Create(ctx, service)).Should(Succeed())
		})

		It("creates rules", func() {
			ctx := context.Background()

			spec1 = &v1beta1.RateLimitRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      spec1Name,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitRuleSpec{
					Domain:          "foo",
					Unit:            "hour",
					RequestsPerUnit: 2,
					Descriptors: []v1beta1.Descriptor{
						{
							Key:   "level-1",
							Value: "value-1",
						},
						{
							Key: "level-2",
						},
					},
				},
			}
			spec2 = &v1beta1.RateLimitRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      spec2Name,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitRuleSpec{
					Domain:          "foo",
					Unit:            "minute",
					RequestsPerUnit: 3,
					Descriptors: []v1beta1.Descriptor{
						{
							Key:   "level-1",
							Value: "value-1",
						},
						{
							Key: "another-level",
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, spec1)).Should(Succeed())
			Expect(k8sClient.Create(ctx, spec2)).Should(Succeed())
		})

		It("should update the service status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: serviceName, Namespace: "default"}
			reconciledInstance := &v1beta1.RateLimitService{}

			expectedStatus := &v1beta1.RateLimitServiceStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/ratelimit-%s created", serviceName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal([]v1beta1.ResourceReference{
				{
					Kind:       "RateLimitRule",
					Name:       spec2Name,
					APIVersion: "ratelimit.infra.doodle.com/v1beta1",
				},
				{
					Kind:       "RateLimitRule",
					Name:       spec1Name,
					APIVersion: "ratelimit.infra.doodle.com/v1beta1",
				},
			}))
		})

		It("should create a service", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("ratelimit-%s", serviceName), Namespace: "default"}
			reconciledInstance := &corev1.Service{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Selector).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(serviceName))
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("ratelimit-%s", serviceName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("ratelimit"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("docker.io/envoyproxy/ratelimit:master"))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(serviceName))
		})

		It("should create a configmap", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("ratelimit-%s", serviceName), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, cm)
			}, timeout, interval).Should(BeNil())

			Expect(cm.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))

			expectedConfig := `domain: foo
descriptors:
- key: level-1
  value: value-1
  descriptors:
  - key: another-level
    rate_limit:
      requests_per_unit: 3
      unit: minute
      name: %s.default
  - key: level-2
    rate_limit:
      requests_per_unit: 2
      unit: hour
      name: %s.default
`
			Expect(cm.Data["foo.yaml"]).To(Equal(fmt.Sprintf(expectedConfig, spec2Name, spec1Name)))
		})

		It("reconciles if a rule is updated", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: spec1Name, Namespace: "default"}
			rule := &v1beta1.RateLimitRule{}

			Eventually(func() error {
				return k8sClient.Get(ctx, key, rule)
			}, timeout, interval).Should(BeNil())

			rule.Spec = v1beta1.RateLimitRuleSpec{
				Domain:          "foo",
				Unit:            "hour",
				RequestsPerUnit: 2,
				ShadowMode:      true,
				DetailedMetric:  true,
				Descriptors: []v1beta1.Descriptor{
					{
						Key:   "level-1",
						Value: "value-1",
					},
					{
						Key:   "level-2",
						Value: "value-2",
					},
				},
			}

			Expect(k8sClient.Update(ctx, rule)).Should(Succeed())
		})

		It("updates configmap", func() {
			key := types.NamespacedName{Name: fmt.Sprintf("ratelimit-%s", serviceName), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			expectedConfig := `domain: foo
descriptors:
- key: level-1
  value: value-1
  descriptors:
  - key: another-level
    rate_limit:
      requests_per_unit: 3
      unit: minute
      name: %s.default
  - key: level-2
    value: value-2
    rate_limit:
      requests_per_unit: 2
      unit: hour
      name: %s.default
    shadow_mode: true
    detailed_metric: true
`

			Eventually(func() error {
				err := k8sClient.Get(ctx, key, cm)
				if err != nil {
					return err
				}

				if cm.Data["foo.yaml"] != fmt.Sprintf(expectedConfig, spec2Name, spec1Name) {
					return fmt.Errorf("expected config does not match, %s!=%s", cm.Data["foo.yaml"], fmt.Sprintf(expectedConfig, spec2Name, spec1Name))
				}

				return nil
			}, timeout, interval).Should(BeNil())
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, service)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, spec1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, spec2)).Should(Succeed())
		})
	})

	When("it reconciles a service with rules which have duplicate descriptor trees", func() {
		serviceName := fmt.Sprintf("service-%s", randStringRunes(5))
		spec1Name := fmt.Sprintf("spec-b-%s", randStringRunes(5))
		spec2Name := fmt.Sprintf("spec-a-%s", randStringRunes(5))
		var service *v1beta1.RateLimitService
		var spec1 *v1beta1.RateLimitRule
		var spec2 *v1beta1.RateLimitRule

		It("creates a new service", func() {
			ctx := context.Background()

			service = &v1beta1.RateLimitService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitServiceSpec{
					RuleSelector: &metav1.LabelSelector{},
				},
			}
			Expect(k8sClient.Create(ctx, service)).Should(Succeed())
		})

		It("creates rules", func() {
			ctx := context.Background()

			spec1 = &v1beta1.RateLimitRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      spec1Name,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitRuleSpec{
					Domain:          "foo",
					Unit:            "hour",
					RequestsPerUnit: 2,
					Descriptors: []v1beta1.Descriptor{
						{
							Key:   "level-1",
							Value: "value-1",
						},
						{
							Key: "level-2",
						},
					},
				},
			}
			spec2 = &v1beta1.RateLimitRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      spec2Name,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitRuleSpec{
					Domain:          "foo",
					Unit:            "minute",
					RequestsPerUnit: 3,
					Descriptors: []v1beta1.Descriptor{
						{
							Key:   "level-1",
							Value: "value-1",
						},
						{
							Key: "level-2",
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, spec1)).Should(Succeed())
			Expect(k8sClient.Create(ctx, spec2)).Should(Succeed())
		})

		It("should update the service status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: serviceName, Namespace: "default"}
			reconciledInstance := &v1beta1.RateLimitService{}

			expectedStatus := &v1beta1.RateLimitServiceStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionFalse,
						Reason:  "ReconciliationFailed",
						Message: fmt.Sprintf("duplicate descriptor path found, can not add rule with identical descriptor path to the same domain and service: %s.default", spec1Name),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal([]v1beta1.ResourceReference{
				{
					Kind:       "RateLimitRule",
					Name:       spec2Name,
					APIVersion: "ratelimit.infra.doodle.com/v1beta1",
				},
				{
					Kind:       "RateLimitRule",
					Name:       spec1Name,
					APIVersion: "ratelimit.infra.doodle.com/v1beta1",
				},
			}))
		})
	})

	When("it reconciles a service which would manage a deployment with the same name", func() {
		serviceName := fmt.Sprintf("service-%s", randStringRunes(5))
		var service *v1beta1.RateLimitService

		It("creates a new service", func() {
			ctx := context.Background()

			service = &v1beta1.RateLimitService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitServiceSpec{},
			}
			Expect(k8sClient.Create(ctx, service)).Should(Succeed())
		})

		It("creates a new existing unmanaged deployment", func() {
			ctx := context.Background()
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("ratelimit-%s", serviceName),
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"x": "foo",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "foo",
									Image: "bar",
								},
							},
						},
					},
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"x": "foo",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).Should(Succeed())
		})

		It("fails to reconcile", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: serviceName, Namespace: "default"}
			reconciledInstance := &v1beta1.RateLimitService{}

			expectedStatus := &v1beta1.RateLimitServiceStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionFalse,
						Reason:  "ReconciliationFailed",
						Message: fmt.Sprintf("can not take ownership of existing deployment: ratelimit-%s", serviceName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(len(reconciledInstance.Status.SubResourceCatalog)).Should(Equal(0))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, service)).Should(Succeed())
		})
	})

	When("it reconciles a service with a custom template", func() {
		serviceName := fmt.Sprintf("service-%s", randStringRunes(5))
		var service *v1beta1.RateLimitService
		var replicas int32 = 3

		It("creates a new service", func() {
			ctx := context.Background()

			service = &v1beta1.RateLimitService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: "default",
				},
				Spec: v1beta1.RateLimitServiceSpec{
					DeploymentTemplate: &v1beta1.DeploymentTemplate{
						Spec: v1beta1.DeploymentSpec{
							Replicas: &replicas,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name: "ratelimit",
											Env: []corev1.EnvVar{
												{
													Name:  "FOO",
													Value: "bar",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, service)).Should(Succeed())
		})

		It("should update the service status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: serviceName, Namespace: "default"}
			reconciledInstance := &v1beta1.RateLimitService{}

			expectedStatus := &v1beta1.RateLimitServiceStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/ratelimit-%s created", serviceName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(len(reconciledInstance.Status.SubResourceCatalog)).Should(Equal(0))
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("ratelimit-%s", serviceName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance":   "ratelimit",
				"app.kubernetes.io/name":       "ratelimit",
				"ratelimit-controller/service": serviceName,
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("ratelimit"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("docker.io/envoyproxy/ratelimit:master"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
				{
					Name:  "FOO",
					Value: "bar",
				},
				{
					Name:  "RUNTIME_SUBDIRECTORY",
					Value: "runtime",
				},
				{
					Name:  "RUNTIME_ROOT",
					Value: "/data",
				},
				{
					Name:  "RUNTIME_WATCH_ROOT",
					Value: "false",
				},
				{
					Name:  "RUNTIME_IGNOREDOTFILES",
					Value: "true",
				},
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(serviceName))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, service)).Should(Succeed())
		})
	})
})
