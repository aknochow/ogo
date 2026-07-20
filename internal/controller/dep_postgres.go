/*
Copyright 2026.

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
	"crypto/rand"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

const (
	componentPostgres = "embedded-postgresql"
	pgUser            = "openshell"
	pgDatabase        = "openshell"
)

type PostgreSQLReconciler struct {
	client.Client
}

func (p *PostgreSQLReconciler) Name() string { return "DatabaseReady" }

func (p *PostgreSQLReconciler) Enabled(_ context.Context, gw *ogov1alpha1.OpenShellGateway) bool {
	return gw.Spec.Database.Embedded && gw.Spec.Database.SecretName == ""
}

func (p *PostgreSQLReconciler) Reconcile(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (metav1.Condition, error) {
	log := logf.FromContext(ctx)
	ns := gatewayNamespace(gw)
	labels := ownershipLabels(componentPostgres, gw)

	password, err := p.ensurePasswordSecret(ctx, gw, ns, labels)
	if err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionDatabaseReady, Status: metav1.ConditionFalse,
			Reason: "ProvisionFailed", Message: fmt.Sprintf("Failed to create password secret: %v", err),
		}, err
	}

	if err := p.ensurePVC(ctx, gw, ns, labels); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionDatabaseReady, Status: metav1.ConditionFalse,
			Reason: "ProvisionFailed", Message: fmt.Sprintf("Failed to create PVC: %v", err),
		}, err
	}

	if err := p.ensureDeployment(ctx, gw, ns, labels); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionDatabaseReady, Status: metav1.ConditionFalse,
			Reason: "ProvisionFailed", Message: fmt.Sprintf("Failed to create deployment: %v", err),
		}, err
	}

	if err := p.ensureService(ctx, gw, ns, labels); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionDatabaseReady, Status: metav1.ConditionFalse,
			Reason: "ProvisionFailed", Message: fmt.Sprintf("Failed to create service: %v", err),
		}, err
	}

	if err := p.ensureURISecret(ctx, gw, ns, labels, password); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionDatabaseReady, Status: metav1.ConditionFalse,
			Reason: "ProvisionFailed", Message: fmt.Sprintf("Failed to create URI secret: %v", err),
		}, err
	}

	log.Info("Embedded PostgreSQL provisioned", "namespace", ns)
	return metav1.Condition{
		Type: ogov1alpha1.ConditionDatabaseReady, Status: metav1.ConditionTrue,
		Reason: "EmbeddedProvisioned", Message: "Dev-only embedded PostgreSQL is running (not for production)",
	}, nil
}

func (p *PostgreSQLReconciler) Cleanup(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	log := logf.FromContext(ctx)
	ns := gatewayNamespace(gw)

	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg", Namespace: ns}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg", Namespace: ns}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg-password", Namespace: ns}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg-uri", Namespace: ns}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg-data", Namespace: ns}},
	} {
		existing := obj.DeepCopyObject().(client.Object)
		if err := p.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing); err == nil {
			if isOwnedByOGO(existing.GetLabels()) {
				if err := p.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
					log.Error(err, "Failed to delete embedded PG resource", "name", obj.GetName())
				}
			}
		}
	}
	return nil
}

func (p *PostgreSQLReconciler) ensurePasswordSecret(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, ns string, labels map[string]string) (string, error) {
	secretName := gw.Name + "-pg-password"
	existing := &corev1.Secret{}
	err := p.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing)
	if err == nil {
		return string(existing.Data["password"]), nil
	}
	if !errors.IsNotFound(err) {
		return "", fmt.Errorf("checking password secret: %w", err)
	}

	password := generatePassword()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Labels: labels},
		Data:       map[string][]byte{"password": []byte(password)},
	}
	return password, p.Create(ctx, secret)
}

func (p *PostgreSQLReconciler) ensurePVC(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, ns string, labels map[string]string) error {
	storageSize := "1Gi"
	if gw.Spec.Database.EmbeddedConfig != nil && gw.Spec.Database.EmbeddedConfig.StorageSize != "" {
		storageSize = gw.Spec.Database.EmbeddedConfig.StorageSize
	}

	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg-data", Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, p.Client, pvc, func() error {
		if pvc.CreationTimestamp.IsZero() {
			pvc.Labels = labels
			pvc.Spec = corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(storageSize)},
				},
			}
		}
		return nil
	})
	return err
}

func (p *PostgreSQLReconciler) ensureDeployment(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, ns string, labels map[string]string) error {
	image := "registry.redhat.io/rhel9/postgresql-16:latest"
	if gw.Spec.Database.EmbeddedConfig != nil && gw.Spec.Database.EmbeddedConfig.Image != "" {
		image = gw.Spec.Database.EmbeddedConfig.Image
	}

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg", Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, p.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Annotations = map[string]string{
			"ogo.aknochow.io/dev-only": "true",
			"ogo.aknochow.io/warning":  "Not for production use",
		}
		deploy.Spec.Replicas = ptr.To(int32(1))
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": gw.Name + "-pg"}}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": gw.Name + "-pg"}},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "postgresql",
					Image: image,
					Env: []corev1.EnvVar{
						{Name: "POSTGRESQL_USER", Value: pgUser},
						{Name: "POSTGRESQL_PASSWORD", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: gw.Name + "-pg-password"},
								Key:                  "password",
							},
						}},
						{Name: "POSTGRESQL_DATABASE", Value: pgDatabase},
					},
					Ports: []corev1.ContainerPort{{ContainerPort: 5432, Protocol: corev1.ProtocolTCP}},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "pgdata", MountPath: "/var/lib/pgsql/data"},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(5432)},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       10,
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(5432)},
						},
						InitialDelaySeconds: 30,
						PeriodSeconds:       30,
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false),
						RunAsNonRoot:             ptr.To(true),
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
				}},
				Volumes: []corev1.Volume{
					{Name: "pgdata", VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: gw.Name + "-pg-data"},
					}},
				},
			},
		}
		return nil
	})
	return err
}

func (p *PostgreSQLReconciler) ensureService(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, ns string, labels map[string]string) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-pg", Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, p.Client, svc, func() error {
		svc.Labels = labels
		svc.Spec = corev1.ServiceSpec{
			Selector: map[string]string{"app": gw.Name + "-pg"},
			Ports: []corev1.ServicePort{
				{Port: 5432, TargetPort: intstr.FromInt32(5432), Protocol: corev1.ProtocolTCP},
			},
		}
		return nil
	})
	return err
}

func (p *PostgreSQLReconciler) ensureURISecret(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, ns string, labels map[string]string, password string) error {
	secretName := gw.Name + "-pg-uri"
	existing := &corev1.Secret{}
	err := p.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking URI secret: %w", err)
	}

	svcName := gw.Name + "-pg"
	uri := fmt.Sprintf("postgresql://%s:%s@%s.%s.svc:5432/%s?sslmode=disable", pgUser, password, svcName, ns, pgDatabase)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Labels: labels},
		Data:       map[string][]byte{"uri": []byte(uri)},
	}
	return p.Create(ctx, secret)
}

func generatePassword() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
