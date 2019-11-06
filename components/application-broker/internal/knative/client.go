package knative

import (
	"context"
	"log"
	"reflect"
	"time"

	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8spkglabels "k8s.io/apimachinery/pkg/labels"
	k8sclientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	eventingv1alpha1 "knative.dev/eventing/pkg/apis/eventing/v1alpha1"
	messagingv1alpha1 "knative.dev/eventing/pkg/apis/messaging/v1alpha1"
	kneventingclientset "knative.dev/eventing/pkg/client/clientset/versioned"
	eventingv1alpha1client "knative.dev/eventing/pkg/client/clientset/versioned/typed/eventing/v1alpha1"
	messagingv1alpha1client "knative.dev/eventing/pkg/client/clientset/versioned/typed/messaging/v1alpha1"
	kneventinginformers "knative.dev/eventing/pkg/client/informers/externalversions"
	messagingv1alpha1listers "knative.dev/eventing/pkg/client/listers/messaging/v1alpha1"
)

const (
	informerSyncTimeout = time.Second * 5
)

// todo
type Client interface {
	GetChannelByLabels(ns string, labels map[string]string) (*messagingv1alpha1.Channel, error)
	GetSubscriptionByLabels(ns string, labels map[string]string) (*messagingv1alpha1.Subscription, error)
	CreateSubscription(*messagingv1alpha1.Subscription) (*messagingv1alpha1.Subscription, error)
	UpdateSubscription(*messagingv1alpha1.Subscription) (*messagingv1alpha1.Subscription, error)
	DeleteSubscription(*messagingv1alpha1.Subscription) error
	GetDefaultBroker(ns string) (*eventingv1alpha1.Broker, error)
	DeleteBroker(*eventingv1alpha1.Broker) error
	GetNamespace(name string) (*corev1.Namespace, error)
	UpdateNamespace(*corev1.Namespace) (*corev1.Namespace, error)
}

// todo
type client struct {
	channelLister   messagingv1alpha1listers.ChannelLister
	messagingClient messagingv1alpha1client.MessagingV1alpha1Interface
	eventingClient  eventingv1alpha1client.EventingV1alpha1Interface
	coreClient      corev1client.CoreV1Interface
}

// compile time contract check
var _ Client = &client{}

// todo
func NewClient(config *rest.Config) (Client, error) {
	// init the Knative client set
	knClientSet, err := kneventingclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	// init the Kubernetes client set
	k8sClientSet, err := k8sclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	// init the Knative informer factory
	informerFactory := kneventinginformers.NewSharedInformerFactory(knClientSet, 0)

	// init the client
	cli := &client{
		channelLister:   informerFactory.Messaging().V1alpha1().Channels().Lister(),
		messagingClient: knClientSet.MessagingV1alpha1(),
		eventingClient:  knClientSet.EventingV1alpha1(),
		coreClient:      k8sClientSet.CoreV1(),
	}

	// start informer factory
	log.Println("starting Knative informer Factory")
	stop := make(chan struct{})
	informerFactory.Start(stop)
	waitForInformersSyncOrDie(informerFactory)

	return cli, nil
}

// GetChannelByLabels return a knative Channel fetched via label selectors
// and based on the labels, the list of channels should have only one item.
func (c *client) GetChannelByLabels(ns string, labels map[string]string) (*messagingv1alpha1.Channel, error) {
	// check there are labels
	if len(labels) == 0 {
		return nil, errors.New("error no labels are provided")
	}

	// list channels
	channelList, err := c.channelLister.Channels(ns).List(k8spkglabels.SelectorFromSet(labels))
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
func (c *client) GetSubscriptionByLabels(ns string, labels map[string]string) (*messagingv1alpha1.Subscription, error) {
	// check there are labels
	if len(labels) == 0 {
		return nil, errors.New("error no labels are provided")
	}

	// list subscriptions
	opts := metav1.ListOptions{
		LabelSelector: k8spkglabels.SelectorFromSet(labels).String(),
	}
	subscriptionList, err := c.messagingClient.Subscriptions(ns).List(opts)
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
func (c *client) CreateSubscription(subscription *messagingv1alpha1.Subscription) (*messagingv1alpha1.Subscription, error) {
	return c.messagingClient.Subscriptions(subscription.Namespace).Create(subscription)
}

// todo
func (c *client) UpdateSubscription(subscription *messagingv1alpha1.Subscription) (*messagingv1alpha1.Subscription, error) {
	return c.messagingClient.Subscriptions(subscription.Namespace).Update(subscription)
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

// DeleteSubscription deletes the given Subscription from the cluster.
func (c *client) DeleteSubscription(s *messagingv1alpha1.Subscription) error {
	bgDeletePolicy := metav1.DeletePropagationBackground

	return c.messagingClient.
		Subscriptions(s.Namespace).
		Delete(s.Name, &metav1.DeleteOptions{PropagationPolicy: &bgDeletePolicy})
}

// GetDefaultBroker gets the default Broker in the given Namespace.
func (c *client) GetDefaultBroker(ns string) (*eventingv1alpha1.Broker, error) {
	return c.eventingClient.Brokers(ns).Get("default", metav1.GetOptions{})
}

// DeleteBroker deletes the given Broker from the cluster.
func (c *client) DeleteBroker(b *eventingv1alpha1.Broker) error {
	bgDeletePolicy := metav1.DeletePropagationBackground

	return c.eventingClient.
		Brokers(b.Namespace).
		Delete(b.Name, &metav1.DeleteOptions{PropagationPolicy: &bgDeletePolicy})
}

// GetNamespace gets a Namespace by name from the cluster.
func (c *client) GetNamespace(name string) (*corev1.Namespace, error) {
	return c.coreClient.Namespaces().Get(name, metav1.GetOptions{})
}

// UpdateNamespace updates the given Namespace in the cluster.
func (c *client) UpdateNamespace(ns *corev1.Namespace) (*corev1.Namespace, error) {
	return c.coreClient.Namespaces().Update(ns)
}
