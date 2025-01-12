package model

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/samber/lo"

	ollamav1 "github.com/nekomeowww/ollama-operator/api/v1"
)

func ModelAppName(name string) string {
	return fmt.Sprintf("ollama-model-%s", name)
}

func getDeployment(ctx context.Context, c client.Client, namespace string, name string) (*appsv1.Deployment, error) {
	var deployment appsv1.Deployment

	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ModelAppName(name)}, &deployment)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return &deployment, nil
}

func EnsureDeploymentCreated(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
	image string,
	replicas *int32,
	model *ollamav1.Model,
	modelRecorder *WrappedRecorder[*ollamav1.Model],
) (*appsv1.Deployment, error) {
	deployment, err := getDeployment(ctx, c, namespace, name)
	if err != nil {
		return nil, err
	}
	if deployment != nil {
		return deployment, nil
	}

	deployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{},
			Annotations: map[string]string{},
			Name:        ModelAppName(name),
			Namespace:   namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         model.APIVersion,
				Kind:               model.Kind,
				Name:               model.Name,
				UID:                model.UID,
				BlockOwnerDeletion: lo.ToPtr(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: lo.Ternary(replicas == nil, lo.ToPtr(int32(1)), replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": ModelAppName(name),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": ModelAppName(name),
					},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						NewOllamaPullerContainer(image, namespace),
					},
					Containers: []corev1.Container{
						NewOllamaServerContainer(true),
					},
					Volumes: []corev1.Volume{
						{
							Name: "image-storage",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: imageStorePVCName,
									ReadOnly:  true,
								},
							},
						},
					},
				},
			},
		},
	}

	err = c.Create(ctx, deployment)
	if err != nil {
		return nil, err
	}

	modelRecorder.Eventf("Normal", "DeploymentCreated", "Deployment %s created", deployment.Name)

	return deployment, nil
}

func IsDeploymentReady(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
	modelRecorder *WrappedRecorder[*ollamav1.Model],
) (bool, error) {
	log := log.FromContext(ctx)

	deployment, err := getDeployment(ctx, c, namespace, name)
	if err != nil {
		return false, err
	}
	if deployment == nil {
		return false, nil
	}

	replica := 1
	if deployment.Spec.Replicas != nil {
		replica = int(*deployment.Spec.Replicas)
	}
	if deployment.Status.ReadyReplicas == int32(replica) {
		log.Info("deployment is ready", "deployment", deployment)
		return true, nil
	}

	log.Info("waiting for deployment to be ready", "deployment", deployment)
	modelRecorder.Eventf("Normal", "WaitingForDeployment", "Waiting for deployment %s to become ready", deployment.Name)

	return false, nil
}

func UpdateDeployment(
	ctx context.Context,
	c client.Client,
	model *ollamav1.Model,
	modelRecorder *WrappedRecorder[*ollamav1.Model],
) (bool, error) {
	deployment, err := getDeployment(ctx, c, model.Namespace, model.Name)
	if err != nil {
		return false, err
	}
	if deployment == nil {
		return false, nil
	}

	replicas := 1

	if model.Spec.Replicas != nil {
		replicas = int(*model.Spec.Replicas)
	}
	if deployment.Spec.Replicas != nil {
		if int(*deployment.Spec.Replicas) == replicas {
			return false, nil
		}

		deployment.Spec.Replicas = lo.ToPtr(int32(replicas))
	} else {
		deployment.Spec.Replicas = lo.ToPtr(int32(replicas))
	}

	err = c.Update(ctx, deployment)
	if err != nil {
		return false, err
	}

	modelRecorder.Eventf(corev1.EventTypeNormal, "ModelScaled", "Model scaled from %d to %d", deployment.Status.Replicas, replicas)

	return true, nil
}

func getService(ctx context.Context, c client.Client, namespace string, name string) (*corev1.Service, error) {
	var service corev1.Service

	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ModelAppName(name)}, &service)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return &service, nil
}

func EnsureServiceCreated(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
	deployment *appsv1.Deployment,
	modelRecorder *WrappedRecorder[*ollamav1.Model],
) (*corev1.Service, error) {
	service, err := getService(ctx, c, namespace, name)
	if err != nil {
		return nil, err
	}
	if service != nil {
		return service, nil
	}

	service = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{},
			Annotations: map[string]string{},
			Name:        ModelAppName(name),
			Namespace:   namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "apps/v1",
				Kind:               "Deployment",
				Name:               deployment.Name,
				UID:                deployment.UID,
				BlockOwnerDeletion: lo.ToPtr(true),
			}},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app": ModelAppName(name),
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "ollama",
					Port:       11434,
					TargetPort: intstr.FromInt(11434),
				},
			},
		},
	}

	err = c.Create(ctx, service)
	if err != nil {
		return nil, err
	}

	modelRecorder.Eventf("Normal", "ServiceCreated", "Service %s created", service.Name)

	return service, nil
}

func IsServiceReady(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
	modelRecorder *WrappedRecorder[*ollamav1.Model],
) (bool, error) {
	log := log.FromContext(ctx)

	service, err := getService(ctx, c, namespace, name)
	if err != nil {
		return false, err
	}
	if service == nil {
		return false, nil
	}
	if service.Spec.ClusterIP == "" {
		log.Info("waiting for service to have cluster IP", "service", service)
		modelRecorder.Eventf("Normal", "WaitingForService", "Waiting for service %s to have cluster IP", service.Name)

		return false, nil
	}

	log.Info("service is ready", "service", service)

	return true, nil
}
