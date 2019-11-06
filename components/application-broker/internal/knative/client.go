package knative

import (
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8spkglabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"

	kneventingapisv1alpha1 "knative.dev/eventing/pkg/apis/messaging/v1alpha1"
	kneventingclientset "knative.dev/eventing/pkg/client/clientset/versioned"
	kneventingclientsetv1alpha1 "knative.dev/eventing/pkg/client/clientset/versioned/typed/messaging/v1alpha1"
	kneventinginformers "knative.dev/eventing/pkg/client/informers/externalversions"
	kneventinglistersv1alpha1 "knative.dev/eventing/pkg/client/listers/messaging/v1alpha1"
)

// todo
type Client interface {
	GetChannelByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Channel, error)
	GetSubscriptionByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Subscription, error)
	CreateSubscription(*kneventingapisv1alpha1.Subscription) (*kneventingapisv1alpha1.Subscription, error)
	UpdateSubscription(*kneventingapisv1alpha1.Subscription) (*kneventingapisv1alpha1.Subscription, error)
}

// todo
type KnClient struct {
	logger          logrus.FieldLogger
	channelLister   kneventinglistersv1alpha1.ChannelLister
	messagingClient kneventingclientsetv1alpha1.MessagingV1alpha1Interface
}

// compile time contract check
var _ Client = &KnClient{}

// todo
func NewKnativeClient(config *rest.Config, logger logrus.FieldLogger) (Client, error) {
	// init the Knative client set
	clientSet, err := kneventingclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	// init the Knative informer factory
	informerFactory := kneventinginformers.NewSharedInformerFactory(clientSet, 0)

	// init the Knative client
	knativeClient := &KnClient{
		logger:          logger.WithField("client", "KnativeClient"),
		channelLister:   informerFactory.Messaging().V1alpha1().Channels().Lister(),
		messagingClient: clientSet.MessagingV1alpha1(),
	}
	return knativeClient, nil
}

// GetChannelByLabels return a knative Channel fetched via label selectors
// and based on the labels, the list of channels should have only one item.
func (k *KnClient) GetChannelByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Channel, error) {
	// check there are labels
	if len(labels) == 0 {
		return nil, errors.New("error no labels are provided")
	}

	// for debugging only ---> start
	cl1, err1 := k.channelLister.List(k8spkglabels.Nothing())
	k.logger.Printf("[DEBUG-A] knative channels fetched: [%v] error: [%s]", cl1, err1)

	cl2, err2 := k.channelLister.Channels(ns).List(k8spkglabels.Nothing())
	k.logger.Printf("[DEBUG-B] knative channels fetched: [%v] error: [%s]", cl2, err2)
	// for debugging only ---> end

	// list channels
	channelList, err := k.channelLister.Channels(ns).List(k8spkglabels.SelectorFromSet(labels))
	if err != nil {
		k.logger.Printf("error getting channels by labels: %v", err)
		return nil, err
	}
	k.logger.Printf("knative channels fetched: %v", channelList)

	// check channel list length to be 1
	if channelListLength := len(channelList); channelListLength != 1 {
		k.logger.Printf("error found %d channels with labels: %v in namespace: %v", channelListLength, labels, ns)
		return nil, errors.New("error length of channel list is not equal to 1")
	}

	// return the single channel found on the list
	return channelList[0], nil
}

// GetSubscriptionByLabels return a knative Subscription fetched via label selectors
// and based on the labels, the list of subscriptions should have only one item.
func (k *KnClient) GetSubscriptionByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Subscription, error) {
	// check there are labels
	if len(labels) == 0 {
		return nil, errors.New("error no labels are provided")
	}

	// list subscriptions
	opts := metav1.ListOptions{
		LabelSelector: k8spkglabels.SelectorFromSet(labels).String(),
	}
	subscriptionList, err := k.messagingClient.Subscriptions(ns).List(opts)
	if err != nil {
		k.logger.Printf("error getting subscriptions by labels: %v", err)
		return nil, err
	}
	k.logger.Printf("knative subscriptions fetched: %v", subscriptionList)

	// check subscription list length to be 1
	if subscriptionListLength := len(subscriptionList.Items); subscriptionListLength != 1 {
		k.logger.Printf("error found %d subscriptions with labels: %v in namespace: %v", subscriptionListLength, labels, ns)
		return nil, errors.New("error length of subscription list is not equal to 1")
	}

	// return the single subscription found on the list
	return &subscriptionList.Items[0], nil
}

// todo
func (k *KnClient) CreateSubscription(subscription *kneventingapisv1alpha1.Subscription) (*kneventingapisv1alpha1.Subscription, error) {
	return k.messagingClient.Subscriptions(subscription.Namespace).Create(subscription)
}

// todo
func (k *KnClient) UpdateSubscription(subscription *kneventingapisv1alpha1.Subscription) (*kneventingapisv1alpha1.Subscription, error) {
	return k.messagingClient.Subscriptions(subscription.Namespace).Update(subscription)
}
