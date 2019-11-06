package knative

import (
	"context"
	"log"
	"reflect"
	"time"

	"github.com/pkg/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8spkglabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"

	kneventingapisv1alpha1 "knative.dev/eventing/pkg/apis/messaging/v1alpha1"
	kneventingclientset "knative.dev/eventing/pkg/client/clientset/versioned"
	kneventingv1alpha1 "knative.dev/eventing/pkg/client/clientset/versioned/typed/eventing/v1alpha1"
	kneventingclientsetv1alpha1 "knative.dev/eventing/pkg/client/clientset/versioned/typed/messaging/v1alpha1"
	kneventinginformers "knative.dev/eventing/pkg/client/informers/externalversions"
	kneventinglistersv1alpha1 "knative.dev/eventing/pkg/client/listers/messaging/v1alpha1"
)

const (
	informerSyncTimeout = time.Second * 5
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
	channelLister   kneventinglistersv1alpha1.ChannelLister
	messagingClient kneventingclientsetv1alpha1.MessagingV1alpha1Interface
}

// compile time contract check
var _ Client = &KnClient{}

// todo
func NewKnativeClient(config *rest.Config) (*KnClient, error) {
	// init the Knative client set
	clientSet, err := kneventingclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	// init the Knative informer factory
	informerFactory := kneventinginformers.NewSharedInformerFactory(clientSet, 0)

	// init the Knative client
	knativeClient := &KnClient{
		channelLister:   informerFactory.Messaging().V1alpha1().Channels().Lister(),
		messagingClient: clientSet.MessagingV1alpha1(),
	}

	// start informer factory
	log.Println("starting Knative informer Factory")
	stop := make(chan struct{})
	informerFactory.Start(stop)
	waitForInformersSyncOrDie(informerFactory)

	// migration issue
	sub := kneventingv1alpha1.Subscription{}
	_ = sub

	return knativeClient, nil
}

// GetChannelByLabels return a knative Channel fetched via label selectors
// and based on the labels, the list of channels should have only one item.
func (k *KnClient) GetChannelByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Channel, error) {
	// check there are labels
	if len(labels) == 0 {
		return nil, errors.New("error no labels are provided")
	}

	// list channels
	channelList, err := k.channelLister.Channels(ns).List(k8spkglabels.SelectorFromSet(labels))
	if err != nil {
		log.Printf("error getting channels by labels: %s", err)
		return nil, err
	}
	log.Printf("knative channels fetched: %s", channelList)

	// check channel list length to be 1
	if channelListLength := len(channelList); channelListLength != 1 {
		log.Printf("error found %d channels with labels: %s in namespace: %s", channelListLength, labels, ns)
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
		log.Printf("error getting subscriptions by labels: %s", err)
		return nil, err
	}
	log.Printf("knative subscriptions fetched: %s", subscriptionList)

	// check subscription list length to be 1
	if subscriptionListLength := len(subscriptionList.Items); subscriptionListLength != 1 {
		log.Printf("error found %d subscriptions with labels: %s in namespace: %s", subscriptionListLength, labels, ns)
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

// waitForInformersSyncOrDie blocks until all informer caches are synced, or panics after a timeout.
func waitForInformersSyncOrDie(f kneventinginformers.SharedInformerFactory) {
	ctx, cancel := context.WithTimeout(context.Background(), informerSyncTimeout)
	defer cancel()

	err := hasSynced(ctx, f.WaitForCacheSync)
	if err != nil {
		log.Fatalf("Error waiting for caches sync: %s", err)
	}
}

type waitForCacheSyncFunc func(stopCh <-chan struct{}) map[reflect.Type]bool

// hasSynced blocks until the given informer sync waiting function completes. It returns an error if the passed context
// gets canceled.
func hasSynced(ctx context.Context, fn waitForCacheSyncFunc) error {
	// synced gets closed as soon as fn returns
	synced := make(chan struct{})

	// closing stopWait forces fn to return, which happens whenever ctx
	// gets canceled
	stopWait := make(chan struct{})
	defer close(stopWait)

	go func() {
		fn(stopWait)
		close(synced)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-synced:
	}

	return nil
}
