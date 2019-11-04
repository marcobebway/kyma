package broker

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/komkom/go-jsonhash"
	"github.com/kubernetes-sigs/service-catalog/pkg/apis/servicecatalog/v1beta1"
	"github.com/kyma-project/kyma/components/application-broker/internal"
	"github.com/kyma-project/kyma/components/application-broker/internal/access"
	"github.com/kyma-project/kyma/components/application-broker/pkg/apis/applicationconnector/v1alpha1"
	v1client "github.com/kyma-project/kyma/components/application-broker/pkg/client/clientset/versioned/typed/applicationconnector/v1alpha1"
	"github.com/pkg/errors"
	osb "github.com/pmorie/go-open-service-broker-client/v2"
	"github.com/sirupsen/logrus"

	apicorev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8spkglabels "k8s.io/apimachinery/pkg/labels"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	kneventingapisv1alpha1 "knative.dev/eventing/pkg/apis/messaging/v1alpha1"
	kneventingclientsetv1alpha1 "knative.dev/eventing/pkg/client/clientset/versioned/typed/messaging/v1alpha1"
	kneventinglistersv1alpha1 "knative.dev/eventing/pkg/client/listers/messaging/v1alpha1"
	knpkgapis "knative.dev/pkg/apis"
	knpkgapisv1alpha1 "knative.dev/pkg/apis/v1alpha1"
	knpkgkmeta "knative.dev/pkg/kmeta"
)

const (
	integrationNamespace               = "kyma-integration"
	serviceCatalogAPIVersion           = "servicecatalog.k8s.io/v1beta1"
	knativeEventingInjectionLabelKey   = "knative-eventing-injection"
	knativeEventingInjectionLabelValue = "true"
)

// NewProvisioner creates provisioner
func NewProvisioner(instanceInserter instanceInserter, instanceGetter instanceGetter, instanceStateGetter instanceStateGetter, operationInserter operationInserter, operationUpdater operationUpdater, accessChecker access.ProvisionChecker, appSvcFinder appSvcFinder, serviceInstanceGetter serviceInstanceGetter, eaClient v1client.ApplicationconnectorV1alpha1Interface, iStateUpdater instanceStateUpdater,
	operationIDProvider func() (internal.OperationID, error), log logrus.FieldLogger, namespaces typedcorev1.NamespaceInterface, channelLister kneventinglistersv1alpha1.ChannelLister,
	messagingClient kneventingclientsetv1alpha1.MessagingV1alpha1Interface) *ProvisionService {
	return &ProvisionService{
		instanceInserter:      instanceInserter,
		instanceGetter:        instanceGetter,
		instanceStateGetter:   instanceStateGetter,
		instanceStateUpdater:  iStateUpdater,
		operationInserter:     operationInserter,
		operationUpdater:      operationUpdater,
		operationIDProvider:   operationIDProvider,
		accessChecker:         accessChecker,
		appSvcFinder:          appSvcFinder,
		eaClient:              eaClient,
		serviceInstanceGetter: serviceInstanceGetter,
		maxWaitTime:           time.Minute,
		log:                   log.WithField("service", "provisioner"),
		namespaces:            namespaces,
		channelLister:         channelLister,
		messagingClient:       messagingClient,
	}
}

// ProvisionService performs provisioning action
type ProvisionService struct {
	instanceInserter      instanceInserter
	instanceGetter        instanceGetter
	instanceStateUpdater  instanceStateUpdater
	operationInserter     operationInserter
	operationUpdater      operationUpdater
	instanceStateGetter   instanceStateGetter
	operationIDProvider   func() (internal.OperationID, error)
	appSvcFinder          appSvcFinder
	eaClient              v1client.ApplicationconnectorV1alpha1Interface
	accessChecker         access.ProvisionChecker
	serviceInstanceGetter serviceInstanceGetter
	namespaces            typedcorev1.NamespaceInterface
	channelLister         kneventinglistersv1alpha1.ChannelLister
	messagingClient       kneventingclientsetv1alpha1.MessagingV1alpha1Interface

	mu sync.Mutex

	maxWaitTime time.Duration
	log         logrus.FieldLogger
	asyncHook   func()
}

// Provision action
func (svc *ProvisionService) Provision(ctx context.Context, osbCtx osbContext, req *osb.ProvisionRequest) (*osb.ProvisionResponse, *osb.HTTPStatusCodeError) {
	if !req.AcceptsIncomplete {
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr("asynchronous operation mode required")}
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()

	iID := internal.InstanceID(req.InstanceID)
	paramHash := jsonhash.HashS(req.Parameters)

	switch state, err := svc.instanceStateGetter.IsProvisioned(iID); true {
	case err != nil:
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while checking if instance is already provisioned: %v", err))}
	case state:
		if err := svc.compareProvisioningParameters(iID, paramHash); err != nil {
			return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusConflict, ErrorMessage: strPtr(fmt.Sprintf("while comparing provisioning parameters %v: %v", req.Parameters, err))}
		}
		return &osb.ProvisionResponse{Async: false}, nil
	}

	switch opIDInProgress, inProgress, err := svc.instanceStateGetter.IsProvisioningInProgress(iID); true {
	case err != nil:
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while checking if instance is being provisioned: %v", err))}
	case inProgress:
		if err := svc.compareProvisioningParameters(iID, paramHash); err != nil {
			return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusConflict, ErrorMessage: strPtr(fmt.Sprintf("while comparing provisioning parameters %v: %v", req.Parameters, err))}
		}
		opKeyInProgress := osb.OperationKey(opIDInProgress)
		return &osb.ProvisionResponse{Async: true, OperationKey: &opKeyInProgress}, nil
	}

	id, err := svc.operationIDProvider()
	if err != nil {
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while generating ID for operation: %v", err))}
	}
	opID := internal.OperationID(id)

	op := internal.InstanceOperation{
		InstanceID:  iID,
		OperationID: opID,
		Type:        internal.OperationTypeCreate,
		State:       internal.OperationStateInProgress,
		ParamsHash:  paramHash,
	}

	if err := svc.operationInserter.Insert(&op); err != nil {
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while inserting instance operation to storage: %v", err))}
	}

	svcID := internal.ServiceID(req.ServiceID)
	svcPlanID := internal.ServicePlanID(req.PlanID)

	app, err := svc.appSvcFinder.FindOneByServiceID(internal.ApplicationServiceID(req.ServiceID))
	if err != nil {
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while getting application with id: %s to storage: %v", req.ServiceID, err))}
	}

	namespace, err := getNamespaceFromContext(req.Context)
	if err != nil {
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while getting namespace from context %v", err))}
	}

	service, err := getSvcByID(app.Services, internal.ApplicationServiceID(req.ServiceID))
	if err != nil {
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while getting service [%s] from Application [%s]: %v", req.ServiceID, app.Name, err))}
	}

	i := internal.Instance{
		ID:            iID,
		Namespace:     namespace,
		ServiceID:     svcID,
		ServicePlanID: svcPlanID,
		State:         internal.InstanceStatePending,
		ParamsHash:    paramHash,
	}

	if err = svc.instanceInserter.Insert(&i); err != nil {
		return nil, &osb.HTTPStatusCodeError{StatusCode: http.StatusBadRequest, ErrorMessage: strPtr(fmt.Sprintf("while inserting instance to storage: %v", err))}
	}

	opKey := osb.OperationKey(op.OperationID)
	resp := &osb.ProvisionResponse{
		OperationKey: &opKey,
		Async:        true,
	}

	svc.doAsync(iID, opID, app.Name, getApplicationServiceID(req), namespace, service.EventProvider, service.DisplayName)
	return resp, nil
}

func getApplicationServiceID(req *osb.ProvisionRequest) internal.ApplicationServiceID {
	return internal.ApplicationServiceID(req.ServiceID)
}

func (svc *ProvisionService) doAsync(iID internal.InstanceID, opID internal.OperationID, appName internal.ApplicationName, appID internal.ApplicationServiceID, ns internal.Namespace, eventProvider bool, displayName string) {
	go svc.do(iID, opID, appName, appID, ns, eventProvider, displayName)
}

func (svc *ProvisionService) do(iID internal.InstanceID, opID internal.OperationID, appName internal.ApplicationName, appID internal.ApplicationServiceID, ns internal.Namespace, eventProvider bool, displayName string) {
	if svc.asyncHook != nil {
		defer svc.asyncHook()
	}
	canProvisionOutput, err := svc.accessChecker.CanProvision(iID, appID, ns, svc.maxWaitTime)
	svc.log.Infof("Access checker: canProvisionInstance(appName=[%s], appID=[%s], ns=[%s]) returned: canProvisionOutput=[%+v], error=[%v]", appName, appID, ns, canProvisionOutput, err)

	var instanceState internal.InstanceState
	var opState internal.OperationState
	var opDesc string

	if err != nil {
		instanceState = internal.InstanceStateFailed
		opState = internal.OperationStateFailed
		opDesc = fmt.Sprintf("provisioning failed on error: %s", err.Error())
	} else if !canProvisionOutput.Allowed {
		instanceState = internal.InstanceStateFailed
		opState = internal.OperationStateFailed
		opDesc = fmt.Sprintf("Forbidden provisioning instance [%s] for application [name: %s, id: %s] in namespace: [%s]. Reason: [%s]", iID, appName, appID, ns, canProvisionOutput.Reason)
	} else {
		instanceState = internal.InstanceStateSucceeded
		opState = internal.OperationStateSucceeded
		opDesc = "provisioning succeeded"
		if eventProvider {
			// objects to be affected
			var eaObj *v1alpha1.EventActivation
			var namespaceObj *apicorev1.Namespace
			var knSubscriptionObj *kneventingapisv1alpha1.Subscription

			// selectors
			namespace := string(ns)
			applicationID := string(appID)
			applicationName := string(appName)

			// enable events from the application to the namespace
			if namespaceObj, err = svc.enableDefaultKnativeEventingBrokerOnSuccessProvision(namespace); err != nil {
				instanceState = internal.InstanceStateFailed
				opState = internal.OperationStateFailed
				opDesc = fmt.Sprintf("provisioning failed while enabling the default Knative Eventing Broker for namespace: %s on error: %s", namespace, err.Error())
			} else if knSubscriptionObj, err = svc.createKnativeSubscriptionOnSuccessProvision(applicationName, namespace); err != nil {
				instanceState = internal.InstanceStateFailed
				opState = internal.OperationStateFailed
				opDesc = fmt.Sprintf("provisioning failed while creating the Knative Subscription for application: %s and namespace: %s on error: %s", applicationName, namespace, err.Error())
			} else if eaObj, err = svc.createEaOnSuccessProvision(applicationName, applicationID, namespace, displayName, iID); err != nil {
				instanceState = internal.InstanceStateFailed
				opState = internal.OperationStateFailed
				opDesc = fmt.Sprintf("provisioning failed while creating EventActivation on error: %s", err.Error())
			}

			// cleanup the affected resources if any operation failed
			if opState == internal.OperationStateFailed {
				if cleanupErr := svc.cleanupNamespaceOnFailedProvision(namespaceObj); cleanupErr != nil {
					// todo handle error
				}
				if cleanupErr := svc.cleanupKnativeSubscriptionOnFailedProvision(knSubscriptionObj); cleanupErr != nil {
					// todo handle error
				}
				if cleanupErr := svc.cleanupEventActivationOnFailedProvision(eaObj); cleanupErr != nil {
					// todo handle error
				}
			}
		}
	}

	if err := svc.instanceStateUpdater.UpdateState(iID, instanceState); err != nil {
		svc.log.Errorf("Cannot update state of the stored instance [%s]: [%v]\n", iID, err)
	}

	if err := svc.operationUpdater.UpdateStateDesc(iID, opID, opState, &opDesc); err != nil {
		svc.log.Errorf("Cannot update state for ServiceInstance [%s]: [%v]\n", iID, err)
		return
	}
}

func (svc *ProvisionService) createEaOnSuccessProvision(appName, appID, ns string, displayName string, iID internal.InstanceID) (*v1alpha1.EventActivation, error) {
	// instance ID is the serviceInstance.Spec.ExternalID
	si, err := svc.serviceInstanceGetter.GetByNamespaceAndExternalID(ns, string(iID))
	if err != nil {
		return nil, errors.Wrapf(err, "while getting service instance with external id: %q in namespace: %q", iID, ns)
	}
	ea := &v1alpha1.EventActivation{
		TypeMeta: metav1.TypeMeta{
			Kind:       "EventActivation",
			APIVersion: "applicationconnector.kyma-project.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      appID,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: serviceCatalogAPIVersion,
					Kind:       "ServiceInstance",
					Name:       si.Name,
					UID:        si.UID,
				},
			},
		},
		Spec: v1alpha1.EventActivationSpec{
			DisplayName: displayName,
			SourceID:    appName,
		},
	}
	ea, err = svc.eaClient.EventActivations(ns).Create(ea)
	switch {
	case err == nil:
		svc.log.Infof("Created EventActivation: [%s], in namespace: [%s]", appID, ns)
	case apierrors.IsAlreadyExists(err):
		// We perform update action to adjust OwnerReference of the EventActivation after the backup restore.
		if ea, err = svc.ensureEaUpdate(appID, ns, si); err != nil {
			return nil, errors.Wrapf(err, "while ensuring update on EventActivation")
		}
		svc.log.Infof("Updated EventActivation: [%s], in namespace: [%s]", appID, ns)
	default:
		return nil, errors.Wrapf(err, "while creating EventActivation with name: %q in namespace: %q", appID, ns)
	}
	return ea, nil
}

func (svc *ProvisionService) ensureEaUpdate(appID, ns string, si *v1beta1.ServiceInstance) (*v1alpha1.EventActivation, error) {
	ea, err := svc.eaClient.EventActivations(ns).Get(appID, metav1.GetOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "while getting EventActivation with name: %q from namespace: %q", appID, ns)
	}
	ea.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: serviceCatalogAPIVersion,
			Kind:       "ServiceInstance",
			Name:       si.Name,
			UID:        si.UID,
		},
	}
	ea, err = svc.eaClient.EventActivations(ns).Update(ea)
	if err != nil {
		return nil, errors.Wrapf(err, "while updating EventActivation with name: %q in namespace: %q", appID, ns)
	}
	return ea, nil
}

// enableDefaultKnativeEventingBroker enables the Knative Eventing default broker for the given namespace
// by adding the proper label.
func (svc *ProvisionService) enableDefaultKnativeEventingBrokerOnSuccessProvision(ns string) (*apicorev1.Namespace, error) {
	// get the namespace and return error if none exists
	namespace, err := svc.namespaces.Get(ns, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// add the knative-eventing-injection label to the namespace
	namespace.Labels[knativeEventingInjectionLabelKey] = knativeEventingInjectionLabelValue

	// update the namespace
	return svc.namespaces.Update(namespace)
}

// createKnativeSubscription creates a Knative Subscription given the application name and the namespace.
func (svc *ProvisionService) createKnativeSubscriptionOnSuccessProvision(applicationName, ns string) (*kneventingapisv1alpha1.Subscription, error) {
	// query the Knative channel by labels
	labels := make(map[string]string) // todo initialize the labels
	channel, err := svc.getChannelByLabels(integrationNamespace, labels)
	if err != nil {
		return nil, err
	}

	// construct the default broker URI using the given namespace.
	defaultBrokerURI := fmt.Sprintf("http://default-broker.%s", ns)

	// create the Knative subscription given the constructed Knative channel name and the default broker URI.
	subscriptionName := "" // todo construct the name
	subscription := newKnativeSubscription(subscriptionName, ns, channel.Name, defaultBrokerURI)

	// create the Knative subscription
	return svc.messagingClient.Subscriptions(integrationNamespace).Create(subscription)
}

func (svc *ProvisionService) compareProvisioningParameters(iID internal.InstanceID, newHash string) error {
	instance, err := svc.instanceGetter.Get(iID)
	switch {
	case err == nil:
	case IsNotFoundError(err):
		return nil
	default:
		return errors.Wrapf(err, "while getting instance %s from storage", iID)
	}

	if instance.ParamsHash != newHash {
		return errors.Errorf("provisioning parameters hash differs - new %s, old %s, for instance %s", newHash, instance.ParamsHash, iID)
	}

	return nil
}

func (svc *ProvisionService) cleanupNamespaceOnFailedProvision(namespace *apicorev1.Namespace) error {
	// todo
	return nil
}

func (svc *ProvisionService) cleanupKnativeSubscriptionOnFailedProvision(subscription *kneventingapisv1alpha1.Subscription) error {
	// todo
	return nil
}

func (svc *ProvisionService) cleanupEventActivationOnFailedProvision(ea *v1alpha1.EventActivation) error {
	// todo
	return nil
}

func getNamespaceFromContext(contextProfile map[string]interface{}) (internal.Namespace, error) {
	return internal.Namespace(contextProfile["namespace"].(string)), nil
}

func getSvcByID(services []internal.Service, id internal.ApplicationServiceID) (internal.Service, error) {
	for _, svc := range services {
		if svc.ID == id {
			return svc, nil
		}
	}
	return internal.Service{}, errors.Errorf("cannot find service with ID [%s]", id)
}

func strPtr(str string) *string {
	return &str
}

// newKnativeSubscription returns a new Knative Subscription instance.
func newKnativeSubscription(name, namespace, channelName, uri string) *kneventingapisv1alpha1.Subscription {
	subscription := &kneventingapisv1alpha1.Subscription{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Subscription",                   // todo validate
			APIVersion: "messaging.knative.dev/v1alpha1", // todo validate
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				*knpkgkmeta.NewControllerRef(nil), // todo add the ObjectRef
			},
		},
		Spec: kneventingapisv1alpha1.SubscriptionSpec{
			Channel: apicorev1.ObjectReference{
				Kind:       "Channel",                        // todo validate
				APIVersion: "messaging.knative.dev/v1alpha1", // todo validate
				Name:       channelName,
			},
			Subscriber: &knpkgapisv1alpha1.Destination{
				URI: &knpkgapis.URL{
					Path: uri, // todo validate
				},
			},
		},
	}
	return subscription
}

// getChannelByLabels return a knative channel fetched via label selectors
// and based on the labels, the list of channels should have only one item
func (svc *ProvisionService) getChannelByLabels(ns string, labels map[string]string) (*kneventingapisv1alpha1.Channel, error) {
	// check there are labels
	if len(labels) == 0 {
		return nil, errors.New("error no labels are provided")
	}

	// list channels
	channelList, err := svc.channelLister.Channels(ns).List(k8spkglabels.SelectorFromSet(labels))
	if err != nil {
		svc.log.Printf("error getting channels by labels: %v", err)
		return nil, err
	}
	svc.log.Printf("knative channels fetched: %v", channelList)

	// check channel list length to be 1
	if channelListLength := len(channelList); channelListLength != 1 {
		svc.log.Printf("error found %d channels with labels: %v in namespace: %v", channelListLength, labels, ns)
		return nil, errors.New("error length of channel list is not equal to 1")
	}

	// return the single channel found on the list
	return channelList[0], nil
}
