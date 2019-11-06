package knative

import (
	"log"
	"regexp"

	apicorev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kneventingapisv1alpha1 "knative.dev/eventing/pkg/apis/messaging/v1alpha1"
	knpkgapis "knative.dev/pkg/apis"
	knpkgapisv1alpha1 "knative.dev/pkg/apis/v1alpha1"
)

const (
	// maxPrefixLength for limiting the max length of the name prefix
	maxPrefixLength = 10

	// generatedNameSeparator for adding a separator after the generated name prefix
	generatedNameSeparator = "-"
)

type SubscriptionBuilder struct {
	subscription *kneventingapisv1alpha1.Subscription
}

func Subscription(prefix, namespace string) *SubscriptionBuilder {
	// format the name prefix
	prefix = formatPrefix(prefix, generatedNameSeparator, maxPrefixLength)

	// construct the Knative Subscription object
	subscription := &kneventingapisv1alpha1.Subscription{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Subscription",
			APIVersion: "messaging.knative.dev/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix,
			Namespace:    namespace,
		},
	}

	// init a new Subscription builder
	return &SubscriptionBuilder{subscription: subscription}
}

func FromSubscription(subscription *kneventingapisv1alpha1.Subscription) *SubscriptionBuilder {
	// init a new Subscription builder
	return &SubscriptionBuilder{subscription: subscription}
}

func (b *SubscriptionBuilder) Spec(channel *kneventingapisv1alpha1.Channel, subscriberURI string) *SubscriptionBuilder {
	b.subscription.Spec = kneventingapisv1alpha1.SubscriptionSpec{
		Channel: apicorev1.ObjectReference{
			Name:       channel.Name,
			Kind:       channel.Kind,
			APIVersion: channel.APIVersion,
		},
		Subscriber: &knpkgapisv1alpha1.Destination{
			URI: &knpkgapis.URL{
				Path: subscriberURI, // todo validate
			},
		},
	}
	return b
}

func (b *SubscriptionBuilder) Build() *kneventingapisv1alpha1.Subscription {
	return b.subscription
}

// formatPrefix returns a new string for the prefix that is limited in the length, not having special characters, and
// has the separator appended to it in the end.
func formatPrefix(prefix, separator string, length int) string {
	// prepare special characters regex
	reg, err := regexp.Compile("[^a-z0-9]+")
	if err != nil {
		log.Fatal(err)
	}

	// limit the prefix length
	if len(prefix) > length {
		prefix = prefix[:length]
	}

	// remove the special characters and append the separator
	prefix = reg.ReplaceAllString(prefix, "") + separator

	return prefix
}
