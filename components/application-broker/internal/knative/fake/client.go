package fake

import (
	"github.com/kyma-project/kyma/components/application-broker/internal/knative"
	kneventingapisv1alpha1 "knative.dev/eventing/pkg/apis/messaging/v1alpha1"
)

// KnClient is a fake Knative client used for testing.
type KnClient struct{}

// compile time contract check
var _ knative.Client = &KnClient{}

// todo
func NewKnativeClient() knative.Client {
	// init the Knative client
	knativeClient := &KnClient{}
	return knativeClient
}

// todo
func (k *KnClient) GetChannelByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Channel, error) {
	// todo
	return nil, nil
}

// todo
func (k *KnClient) GetSubscriptionByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Subscription, error) {
	// todo
	return nil, nil
}

// todo
func (k *KnClient) CreateSubscription(*kneventingapisv1alpha1.Subscription) (*kneventingapisv1alpha1.Subscription, error) {
	// todo
	return nil, nil
}

// todo
func (k *KnClient) UpdateSubscription(subscription *kneventingapisv1alpha1.Subscription) (*kneventingapisv1alpha1.Subscription, error) {
	// todo
	return nil, nil
}
