package crd

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsscheme "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OwnerOf reports which RestDefinition (namespace/name) owns the live CRD for gr, or "" if the CRD does
// not exist or carries no owner. Errors other than NotFound are returned: a delete path must not read a
// failed GET as "unowned" and proceed to uninstall.
//
// IT LOOKS THE CRD UP EXACTLY AS Uninstall DOES, by gr.String(). That is deliberate -- the point of this
// function is to answer a question about the object Uninstall is ABOUT to delete, so a different lookup
// would make the answer about a different object (issue 763).
func OwnerOf(ctx context.Context, kube client.Client, gr schema.GroupResource) (string, error) {
	if err := registerEventually(); err != nil {
		return "", err
	}

	obj := apiextensionsv1.CustomResourceDefinition{}
	if err := kube.Get(ctx, client.ObjectKey{Name: gr.String()}, &obj, &client.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	return ownerOf(&obj), nil
}

func Uninstall(ctx context.Context, kube client.Client, gr schema.GroupResource) error {
	if err := registerEventually(); err != nil {
		return err
	}

	obj := apiextensionsv1.CustomResourceDefinition{}
	err := kube.Get(ctx, client.ObjectKey{Name: gr.String()}, &obj, &client.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return err
	}

	err = kube.Delete(ctx, &obj, &client.DeleteOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return err
	}

	return nil

}

func Lookup(ctx context.Context, kube client.Client, gvr schema.GroupVersionResource) (bool, error) {
	if err := registerEventually(); err != nil {
		return false, err
	}

	res := apiextensionsv1.CustomResourceDefinition{}
	err := kube.Get(ctx, client.ObjectKey{Name: gvr.GroupResource().String()}, &res, &client.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		} else {
			return false, err
		}
	}

	for _, el := range res.Spec.Versions {
		if el.Name == gvr.Version {
			return true, nil
		}
	}

	return false, nil
}

func Unmarshal(dat []byte) (*apiextensionsv1.CustomResourceDefinition, error) {
	if err := registerEventually(); err != nil {
		return nil, err
	}

	if len(dat) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	s := json.NewYAMLSerializer(json.DefaultMetaFactory,
		clientsetscheme.Scheme,
		clientsetscheme.Scheme)

	res := &apiextensionsv1.CustomResourceDefinition{}
	_, _, err := s.Decode(dat, nil, res)
	return res, err
}

func registerEventually() error {
	if clientsetscheme.Scheme.IsGroupRegistered("apiextensions.k8s.io") {
		return nil
	}

	return apiextensionsscheme.AddToScheme(clientsetscheme.Scheme)
}
