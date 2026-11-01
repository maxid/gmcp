package gmcp

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Subscription is one open subscriptions/listen stream. The server sends every
// notification on it, and ends it either on its own initiative (Finish) or
// because the client cancelled (Close).
type Subscription struct {
	srv    *Server
	id     json.RawMessage
	filter SubscriptionFilter
	emit   func(json.RawMessage) error

	done chan struct{}
	once sync.Once
}

// ID is the JSON-RPC id of the subscriptions/listen request that opened the stream.
func (sub *Subscription) ID() json.RawMessage { return sub.id }

// Filter is the notification set the server agreed to honor, which may be a
// subset of what the client asked for.
func (sub *Subscription) Filter() SubscriptionFilter { return sub.filter }

// Done is closed when the subscription ends.
func (sub *Subscription) Done() <-chan struct{} { return sub.done }

// Close ends the subscription without a completion result; it is what a client
// cancellation leaves behind.
func (sub *Subscription) Close() { sub.finish(false) }

// Finish ends the subscription gracefully: the client receives the empty
// subscriptions/listen result and knows the teardown was clean.
func (sub *Subscription) Finish() { sub.finish(true) }

func (sub *Subscription) finish(graceful bool) {
	sub.once.Do(func() {
		close(sub.done)
		sub.srv.dropSubscription(sub)
		if !graceful {
			return
		}
		_ = sub.emit(mustJSON(Response{
			JSONRPC: JSONRPCVersion,
			ID:      sub.id,
			Result: mustJSON(SubscriptionsListenResult{
				Meta:       &ResultMeta{SubscriptionID: sub.id},
				ResultType: ResultTypeComplete,
			}),
		}))
	})
}

// Notify sends one notification on the subscription. Every notification
// carries the subscription id in _meta, which is how a client multiplexing one
// channel tells subscriptions apart.
func (sub *Subscription) Notify(method string, params any) error {
	select {
	case <-sub.done:
		return fmt.Errorf("gmcp: subscription %s is closed", string(sub.id))
	default:
	}

	payload := map[string]any{}
	if params != nil {
		if err := json.Unmarshal(mustJSON(params), &payload); err != nil {
			return fmt.Errorf("gmcp: notification params: %w", err)
		}
	}
	meta, _ := payload["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[MetaSubscriptionID] = sub.id
	payload["_meta"] = meta

	return sub.emit(mustJSON(Notification{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  mustJSON(payload),
	}))
}

// ToolsListChanged reports a change in the tool list, if the client subscribed to it.
func (sub *Subscription) ToolsListChanged() error {
	if !sub.filter.ToolsListChanged {
		return nil
	}
	return sub.Notify(NotifyToolsListChanged, nil)
}

// PromptsListChanged reports a change in the prompt list, if the client subscribed to it.
func (sub *Subscription) PromptsListChanged() error {
	if !sub.filter.PromptsListChanged {
		return nil
	}
	return sub.Notify(NotifyPromptsListChanged, nil)
}

// ResourcesListChanged reports a change in the resource list, if the client subscribed to it.
func (sub *Subscription) ResourcesListChanged() error {
	if !sub.filter.ResourcesListChanged {
		return nil
	}
	return sub.Notify(NotifyResourcesListChanged, nil)
}

// ResourceUpdated reports a new version of one resource, if the client subscribed to it.
func (sub *Subscription) ResourceUpdated(uri string) error {
	for _, subscribed := range sub.filter.ResourceSubscriptions {
		if subscribed == uri {
			return sub.Notify(NotifyResourceUpdated, ResourceUpdatedParams{URI: uri})
		}
	}
	return nil
}

// openSubscription acknowledges a subscriptions/listen request, then hands the
// stream to the installed handler. Without a handler the subscription is ended
// right away, so the client never waits on a stream nobody writes to.
func (s *Server) openSubscription(request Request, emit func(json.RawMessage) error) *Subscription {
	params := SubscriptionsListenParams{}
	if len(request.Params) > 0 {
		_ = json.Unmarshal(request.Params, &params)
	}

	granted := s.grantSubscription(params.Notifications)
	sub := &Subscription{
		srv:    s,
		id:     request.ID,
		filter: granted,
		emit:   emit,
		done:   make(chan struct{}),
	}

	ack := Notification{
		JSONRPC: JSONRPCVersion,
		Method:  NotifySubscriptionsAck,
		Params: mustJSON(SubscriptionsAcknowledgedParams{
			Meta:          &ResultMeta{SubscriptionID: request.ID},
			Notifications: granted,
		}),
	}
	s.registerSubscription(sub)

	if err := emit(mustJSON(ack)); err != nil {
		s.dropSubscription(sub)
		return nil
	}

	handler := s.onSubscribe
	if handler == nil {
		sub.Finish()
		return sub
	}
	go func() {
		handler(sub)
		sub.Finish()
	}()
	return sub
}

// grantSubscription returns the subset of the requested filter this server can
// honor. Only tool-list changes exist today.
func (s *Server) grantSubscription(requested SubscriptionFilter) SubscriptionFilter {
	return SubscriptionFilter{ToolsListChanged: requested.ToolsListChanged}
}

func (s *Server) registerSubscription(sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[string(sub.id)] = sub
}

func (s *Server) dropSubscription(sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, string(sub.id))
}

func (s *Server) cancelSubscription(id json.RawMessage) {
	if len(id) == 0 {
		return
	}
	s.mu.RLock()
	sub := s.subs[string(id)]
	s.mu.RUnlock()
	if sub != nil {
		sub.Close()
	}
}

func (s *Server) closeAllSubscriptions(graceful bool) {
	s.mu.RLock()
	subs := make([]*Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.RUnlock()
	for _, sub := range subs {
		sub.finish(graceful)
	}
}
